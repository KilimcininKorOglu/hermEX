package ews

import (
	"errors"
	"slices"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// calendarView is the FindItem <m:CalendarView> element ([MS-OXWSSRCH] 2.2.4.3):
// the window to list and the most entries to return.
type calendarView struct {
	MaxEntries int    `xml:"MaxEntriesReturned,attr"`
	Start      string `xml:"StartDate,attr"`
	End        string `xml:"EndDate,attr"`
}

// occurrenceReach bounds how far from its generated instant an exception can have
// moved an occurrence. Outlook does not let an occurrence pass its neighbours, and
// the widest series step is a year, so two years on either side holds every one.
const occurrenceReach = 2 * 366 * 24 * time.Hour

// errNoOccurrence reports an occurrence id the series does not generate.
var errNoOccurrence = errors.New("ews: no such occurrence")

// window parses the view's range. EndDate before StartDate is refused as Exchange
// refuses it.
func (v *calendarView) window() (start, end time.Time, code string) {
	start, err1 := time.Parse(time.RFC3339, v.Start)
	end, err2 := time.Parse(time.RFC3339, v.End)
	if err1 != nil || err2 != nil {
		return time.Time{}, time.Time{}, "ErrorInvalidRequest"
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, "ErrorCalendarEndDateIsEarlierThanStartDate"
	}
	if end.After(start.AddDate(2, 0, 0)) {
		return time.Time{}, time.Time{}, "ErrorCalendarViewRangeTooBig"
	}
	return start, end, ""
}

// calendarView lists a calendar as it appears in the view's window: each single
// appointment that overlaps it and each occurrence a series places in it, in start
// order. A restriction does not apply to a calendar view, as in Exchange.
func (l itemListing) calendarView(fid int64) findItemResponseMessage {
	if l.filter != nil {
		return findItemError("ErrorInvalidRestriction")
	}
	start, end, code := l.view.window()
	if code != "" {
		return findItemError(code)
	}
	reader, err := newCalendarReader(l.st)
	if err != nil {
		return findItemError("ErrorInternalServerError")
	}
	items, err := reader.inWindow(fid, l.idMailbox, start, end)
	if err != nil {
		return findItemError("ErrorInternalServerError")
	}
	slices.SortStableFunc(items, func(a, b oxews.CalendarItem) int { return compareStart(a.Start, b.Start) })
	last := true
	if l.view.MaxEntries > 0 && len(items) > l.view.MaxEntries {
		items, last = items[:l.view.MaxEntries], false
	}
	for i := range items {
		id, err := oxews.DecodeAnyItemID(items[i].ItemID.ID)
		if err != nil {
			return findItemError("ErrorInternalServerError")
		}
		items[i].ExtendedProperties = readExtended(l.st, id.MessageID, l.fields)
	}
	return findItemFound(&findItemRoot{
		TotalItemsInView:        len(items),
		IncludesLastItemInRange: last,
		Items:                   itemsWrap{CalendarItems: items},
	})
}

// compareStart orders two xs:dateTime values, which this server always writes in
// UTC with one layout, so their text order is their time order.
func compareStart(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// inWindow renders the single appointments and series occurrences of a folder that
// overlap the window.
func (r *calendarReader) inWindow(fid int64, mailbox string, start, end time.Time) ([]oxews.CalendarItem, error) {
	singles, err := r.st.ListFolderObjectsInWindow(fid, r.tags.start, r.tags.end, start, end)
	if err != nil {
		return nil, err
	}
	var items []oxews.CalendarItem
	for _, o := range singles {
		item, ok, err := r.single(oxews.ItemID{FolderID: fid, MessageID: o.ID, Mailbox: mailbox}, start, end)
		if err != nil {
			return nil, err
		}
		if ok {
			items = append(items, item)
		}
	}
	masters, err := r.st.ListFolderObjectsWithFlag(fid, r.recur)
	if err != nil {
		return nil, err
	}
	for _, o := range masters {
		occ, err := r.occurrences(oxews.ItemID{FolderID: fid, MessageID: o.ID, Mailbox: mailbox}, start, end)
		if err != nil {
			return nil, err
		}
		items = append(items, occ...)
	}
	return items, nil
}

// single renders one appointment the store's window listing returned, when it is
// not a series and overlaps the window. The store's window test is wider than the
// overlap test, and a series is listed by its occurrences instead.
func (r *calendarReader) single(id oxews.ItemID, start, end time.Time) (oxews.CalendarItem, bool, error) {
	msg, err := r.st.OpenMessage(id.MessageID)
	if err != nil {
		return oxews.CalendarItem{}, false, err
	}
	c := r.meta(msg)
	if c.Recurring || !c.Start.Before(end) || !c.End.After(start) {
		return oxews.CalendarItem{}, false, nil
	}
	item, err := r.render(id, oxews.EncodeItemID(id), msg, c)
	return item, err == nil, err
}

// occurrences renders the occurrences a series places in the window, each under
// its own occurrence id.
func (r *calendarReader) occurrences(id oxews.ItemID, start, end time.Time) ([]oxews.CalendarItem, error) {
	msg, err := r.st.OpenMessage(id.MessageID)
	if err != nil {
		return nil, err
	}
	occ, ok, err := oxcical.SeriesOccurrences(msg, oxcical.Options{Resolver: r.st.GetNamedPropIDs}, start, end)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("ews: a recurring object does not expand as a series")
	}
	items := make([]oxews.CalendarItem, 0, len(occ))
	for _, o := range occ {
		item, err := r.renderOccurrence(id, msg, o)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// occurrence renders the occurrence an occurrence id names.
func (r *calendarReader) occurrence(id oxews.ItemID, _ string) (oxews.CalendarItem, error) {
	msg, err := r.st.OpenMessage(id.MessageID)
	if err != nil {
		return oxews.CalendarItem{}, err
	}
	at := time.Unix(id.Instance, 0)
	occ, ok, err := oxcical.SeriesOccurrences(msg, oxcical.Options{Resolver: r.st.GetNamedPropIDs},
		at.Add(-occurrenceReach), at.Add(occurrenceReach))
	if err != nil {
		return oxews.CalendarItem{}, err
	}
	if !ok {
		return oxews.CalendarItem{}, errNoOccurrence
	}
	for _, o := range occ {
		if o.At.Equal(at) {
			return r.renderOccurrence(id, msg, o)
		}
	}
	return oxews.CalendarItem{}, errNoOccurrence
}

// renderOccurrence renders one occurrence of a series: the series' facts with the
// span and the details its exception, if any, changes.
func (r *calendarReader) renderOccurrence(id oxews.ItemID, msg *oxcmail.Message, o oxcical.Occurrence) (oxews.CalendarItem, error) {
	c := r.meta(msg)
	c.Start, c.End = o.Start.UTC(), o.End.UTC()
	c.Subject, c.Location = o.Subject, o.Location
	c.BusyStatus = busyTypeName(o.BusyStatus)
	c.ReminderSet = o.ReminderSet
	if !c.ReminderSet {
		c.ReminderMinutes = nil
	}
	c.Recurring = true
	c.OriginalStart = o.At.UTC()
	c.Exception = o.Exception
	id.Instance = o.At.Unix()
	return r.render(id, oxews.EncodeItemID(id), msg, c)
}

// nthOccurrence returns the instant a series generates for its index-th
// occurrence, counted from one ([MS-OXWSCORE] OccurrenceItemId InstanceIndex). An
// occurrence deleted from the series keeps its place in the count, as Exchange
// numbers them, so the index of every later one does not shift. The series is
// expanded a year of generated instants at a time from its first start.
func (r *calendarReader) nthOccurrence(msg *oxcmail.Message, index int) (time.Time, error) {
	if index < 1 {
		return time.Time{}, errNoOccurrence
	}
	ical, err := oxcical.Export(msg, oxcical.Options{Resolver: r.st.GetNamedPropIDs})
	if err != nil {
		return time.Time{}, err
	}
	from, _ := r.span(msg.Props)
	for range 100 {
		to := from.AddDate(1, 0, 0)
		insts, ok := oxcical.GeneratedInstants(ical, from, to)
		if !ok {
			return time.Time{}, errNoOccurrence
		}
		if index <= len(insts) {
			return insts[index-1], nil
		}
		index -= len(insts)
		from = to
	}
	return time.Time{}, errNoOccurrence
}

// isSeries reports whether a stored object is a recurring series master.
func (r *calendarReader) isSeries(p mapi.PropertyValues) bool {
	return boolProp(p, r.recur)
}
