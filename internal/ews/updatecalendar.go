package ews

import (
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/meeting"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// Calendar item fields UpdateItem writes. A field outside this set is refused, as
// for mail, so no edit is accepted and dropped.
const (
	fieldReminderSet     = "item:ReminderIsSet"
	fieldReminderMinutes = "item:ReminderMinutesBeforeStart"
	fieldStart           = "calendar:Start"
	fieldEnd             = "calendar:End"
	fieldLocation        = "calendar:Location"
	fieldBusyStatus      = "calendar:LegacyFreeBusyStatus"
	fieldAllDay          = "calendar:IsAllDayEvent"
)

// calendarFields is the set of fields a calendar item update writes.
var calendarFields = map[string]bool{
	fieldSubject: true, fieldBody: true, fieldReminderSet: true, fieldReminderMinutes: true,
	fieldStart: true, fieldEnd: true, fieldLocation: true, fieldBusyStatus: true, fieldAllDay: true,
}

// calendarEdit is one UpdateItem ItemChange on a stored appointment.
type calendarEdit struct {
	st     *objectstore.Store
	id     oxews.ItemID
	msg    *oxcmail.Message
	item   createCalendarItem // the fields the change sets, merged
	named  map[string]bool    // the FieldURIs the change sets
	send   string             // SendMeetingInvitationsOrCancellations
	caller string
}

// updateCalendarItem applies one ItemChange to a stored appointment or one
// occurrence of a series, and tells the attendees when the caller organizes the
// meeting and the request asks for it.
func (s *Server) updateCalendarItem(st *objectstore.Store, id oxews.ItemID, ch itemChangeReq, send, caller string) itemResponseMessage {
	msg, err := st.OpenMessage(id.MessageID)
	if err != nil {
		return itemError("ErrorItemNotFound")
	}
	e, code := newCalendarEdit(st, id, msg, ch)
	if code == "" && send == "" {
		code = "ErrorSendMeetingInvitationsOrCancellationsRequired"
	}
	if code == "" {
		e.send, e.caller = send, caller
		if id.Instance != 0 {
			code = s.editOccurrence(e)
		} else {
			code = s.editMaster(e)
		}
	}
	if code != "" {
		return itemError(code)
	}
	return itemFound(&itemsWrap{CalendarItems: []oxews.CalendarItem{{
		ItemID: oxews.ItemIDElem{ID: ch.ItemID.ID, ChangeKey: changeKey(st, id.MessageID)},
	}}})
}

// newCalendarEdit reads an ItemChange on a stored appointment, refusing a field it
// does not write and an item that is not an appointment.
func newCalendarEdit(st *objectstore.Store, id oxews.ItemID, msg *oxcmail.Message, ch itemChangeReq) (calendarEdit, string) {
	if !isAppointment(itemClass(msg.Props)) || len(ch.Updates.DeleteFields) > 0 {
		return calendarEdit{}, "ErrorInvalidPropertySet"
	}
	e := calendarEdit{st: st, id: id, msg: msg, named: map[string]bool{}}
	for _, sf := range ch.Updates.SetFields {
		uri := sf.FieldURI.URI
		if sf.Extended != nil || !calendarFields[uri] {
			return calendarEdit{}, "ErrorInvalidPropertySet"
		}
		e.named[uri] = true
		e.item.merge(uri, sf.CalendarItem)
	}
	return e, ""
}

// merge copies the one field uri names from the CalendarItem a SetItemField
// carries; every update repeats the element, so only that field is read.
func (item *createCalendarItem) merge(uri string, from createCalendarItem) {
	switch uri {
	case fieldSubject:
		item.Subject = from.Subject
	case fieldBody:
		item.Body = from.Body
	case fieldReminderSet:
		item.ReminderIsSet = from.ReminderIsSet
	case fieldReminderMinutes:
		item.ReminderMinutes = from.ReminderMinutes
	case fieldStart:
		item.Start = from.Start
	case fieldEnd:
		item.End = from.End
	case fieldLocation:
		item.Location = from.Location
	case fieldBusyStatus:
		item.LegacyFreeBusyStatus = from.LegacyFreeBusyStatus
	case fieldAllDay:
		item.IsAllDayEvent = from.IsAllDayEvent
	}
}

// editOccurrence moves one occurrence of a series to a new span, the only change an
// occurrence takes here, and tells the attendees about that instance.
func (s *Server) editOccurrence(e calendarEdit) string {
	for uri := range e.named {
		if uri != fieldStart && uri != fieldEnd {
			return "ErrorInvalidPropertySet"
		}
	}
	before, ok := verbatimICal(e.msg.Props)
	if !ok {
		return "ErrorItemSave"
	}
	at := time.Unix(e.id.Instance, 0).UTC()
	span, ok := instanceSpan(before, at)
	if !ok {
		return "ErrorCalendarOccurrenceIsDeletedFromRecurrence"
	}
	start, end, code := e.item.newSpan(span, time.UTC)
	if code != "" {
		return code
	}
	edited, ok := oxcical.MoveOccurrence(before, at, start, end)
	if !ok {
		return "ErrorItemNotFound"
	}
	edited, notice := occurrenceUpdate(e, before, edited, at)
	if err := meeting.ReplaceSeries(e.st, e.id.MessageID, edited); err != nil {
		return "ErrorItemSave"
	}
	s.sendScheduling(e.st, notice, e.send)
	return ""
}

// instanceSpan is the span a series' instance generated at at occupies now, ok
// false when the series no longer has it.
func instanceSpan(ical []byte, at time.Time) (oxcical.Span, bool) {
	insts, ok := oxcical.InstancesIn(ical, at.Add(-occurrenceReach), at.Add(occurrenceReach))
	if !ok {
		return oxcical.Span{}, false
	}
	for _, in := range insts {
		if in.At.Equal(at) {
			return in.Span, true
		}
	}
	return oxcical.Span{}, false
}

// occurrenceUpdate advances the moved instance's own revision past the series'
// and prepares the single-instance request describing it as edited, when there is
// anyone to tell.
func occurrenceUpdate(e calendarEdit, before, edited []byte, at time.Time) ([]byte, *schedulingMail) {
	to := meetingAudience(e.st, e.msg, e.caller)
	if len(to) == 0 {
		return edited, nil
	}
	seq := max(oxcical.Sequence(before, nil), oxcical.Sequence(before, &at)) + 1
	revised, ok := oxcical.SetSequence(edited, seq, &at)
	if !ok {
		return edited, nil
	}
	body, ok := oxcical.InstanceBody(revised, at, "REQUEST", seq)
	if !ok {
		return edited, nil
	}
	return revised, &schedulingMail{organizer: e.caller, to: to, subject: strProp(e.msg.Props, mapi.PrSubject), method: "REQUEST", calendar: body}
}

// newSpan is the span the edit gives the item: the start and end it names, the
// current ones for those it does not. An end before the start is refused.
func (item createCalendarItem) newSpan(current oxcical.Span, loc *time.Location) (start, end time.Time, code string) {
	start, end = current.Start, current.End
	if item.Start != "" {
		t, ok := parseEWSTime(item.Start, loc)
		if !ok {
			return start, end, "ErrorInvalidRequest"
		}
		start = t
	}
	if item.End != "" {
		t, ok := parseEWSTime(item.End, loc)
		if !ok {
			return start, end, "ErrorInvalidRequest"
		}
		end = t
	}
	if end.Before(start) {
		return start, end, "ErrorCalendarEndDateIsEarlierThanStartDate"
	}
	return start, end, ""
}

// editMaster rewrites a stored appointment or series from its iCalendar with the
// edit applied, through the import every protocol stores appointments with, then
// writes what the import cannot carry and resends the meeting when asked to.
func (s *Server) editMaster(e calendarEdit) string {
	opt := oxcical.Options{Resolver: e.st.GetNamedPropIDs}
	stored, err := oxcical.Export(e.msg, opt)
	if err != nil {
		return "ErrorItemSave"
	}
	patch, code := e.icalPatch()
	if code != "" {
		return code
	}
	edited := oxcical.CarryExceptions(stored, patchMaster(stored, patch))
	to := meetingAudience(e.st, e.msg, e.caller)
	if len(to) > 0 && e.send != "SendToNone" {
		if revised, ok := oxcical.SetSequence(edited, oxcical.Sequence(stored, nil)+1, nil); ok {
			edited = revised
		}
	}
	if err := rewriteAppointment(e.st, e.id.MessageID, edited, opt); err != nil {
		return "ErrorItemSave"
	}
	if err := e.writeProps(); err != nil {
		return "ErrorItemSave"
	}
	s.resend(e, to)
	return ""
}

// resend sends the edited meeting to its attendees as the request asks.
func (s *Server) resend(e calendarEdit, to []string) {
	if len(to) == 0 || e.send == "SendToNone" {
		return
	}
	m, err := invitation(e.st, e.id.MessageID, e.caller, to)
	if err != nil {
		e.st.LogSwallowedError("ews.meeting_update", err)
		return
	}
	s.sendScheduling(e.st, m, e.send)
}

// icalPatch is the master VEVENT lines the edit replaces, "" for a line it
// removes.
func (e calendarEdit) icalPatch() (map[string]string, string) {
	patch := map[string]string{}
	if e.named[fieldSubject] {
		patch["SUMMARY"] = "SUMMARY:" + icalEscape(e.item.Subject)
	}
	if e.named[fieldLocation] {
		patch["LOCATION"] = ""
		if e.item.Location != "" {
			patch["LOCATION"] = "LOCATION:" + icalEscape(e.item.Location)
		}
	}
	if e.named[fieldBody] {
		patch["DESCRIPTION"] = ""
		if e.item.Body.Content != "" && !strings.EqualFold(e.item.Body.Type, "HTML") {
			patch["DESCRIPTION"] = "DESCRIPTION:" + icalEscape(e.item.Body.Content)
		}
	}
	if !e.named[fieldStart] && !e.named[fieldEnd] && !e.named[fieldAllDay] {
		return patch, ""
	}
	return e.timePatch(patch)
}

// timePatch adds the DTSTART and DTEND lines of an edit that moves the item or
// makes it all-day, in the zone the item is shown in.
func (e calendarEdit) timePatch(patch map[string]string) (map[string]string, string) {
	reader, err := newCalendarReader(e.st)
	if err != nil {
		return nil, "ErrorInternalServerError"
	}
	loc := oxcical.DisplayZone(e.msg.Props, oxcical.Options{Resolver: e.st.GetNamedPropIDs})
	if loc == nil {
		loc = time.UTC
	}
	cs, ce := reader.span(e.msg.Props)
	start, end, code := e.item.newSpan(oxcical.Span{Start: cs, End: ce}, loc)
	if code != "" {
		return nil, code
	}
	allDay := boolProp(e.msg.Props, reader.tags.allDay)
	if e.named[fieldAllDay] {
		allDay = e.item.IsAllDayEvent
	}
	patch["DTSTART"] = "DTSTART" + icalWhen(start, allDay, loc)
	patch["DTEND"] = "DTEND" + icalWhen(end, allDay, loc)
	return patch, ""
}

// writeProps writes what the iCalendar import cannot carry: the busy status, the
// reminder and an HTML body.
func (e calendarEdit) writeProps() error {
	ids, err := e.st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameBusyStatus, mapi.NameReminderSet, mapi.NameReminderDelta})
	if err != nil {
		return err
	}
	set, err := e.namedValues(ids)
	if err != nil {
		return err
	}
	var removed []mapi.PropTag
	if e.named[fieldBody] {
		if strings.EqualFold(e.item.Body.Type, "HTML") {
			set.Set(mapi.PrHTML, []byte(oxews.ToCRLF(e.item.Body.Content)))
		} else {
			removed = append(removed, mapi.PrHTML)
		}
	}
	if len(set) == 0 && len(removed) == 0 {
		return nil
	}
	return e.st.ModifyMessageProperties(e.id.MessageID, set, removed...)
}

// namedValues are the busy status and reminder values the edit sets, under the
// busy status, reminder-set and reminder-delta ids.
func (e calendarEdit) namedValues(ids []uint16) (mapi.PropertyValues, error) {
	var set mapi.PropertyValues
	if e.named[fieldBusyStatus] {
		v, ok := busyStatusValues[e.item.LegacyFreeBusyStatus]
		if !ok {
			return nil, errCalendarField
		}
		set.Set(mapi.MakeTag(ids[0], mapi.PtLong), v)
	}
	if e.named[fieldReminderSet] && e.item.ReminderIsSet != nil {
		set.Set(mapi.MakeTag(ids[1], mapi.PtBoolean), *e.item.ReminderIsSet)
	}
	if e.named[fieldReminderMinutes] && e.item.ReminderMinutes != nil {
		set.Set(mapi.MakeTag(ids[2], mapi.PtLong), int32(*e.item.ReminderMinutes)) // #nosec G115 -- an xs:int reminder fits a MAPI long
	}
	return set, nil
}

// rewriteAppointment imports ics and writes it over the stored appointment in
// place: the managed properties it sets, the removal of those it does not, and
// its attendees.
func rewriteAppointment(st *objectstore.Store, id int64, ics []byte, opt oxcical.Options) error {
	msg, err := oxcical.Import(ics, opt)
	if err != nil {
		return err
	}
	managed, err := oxcical.ManagedTags(opt)
	if err != nil {
		return err
	}
	var absent []mapi.PropTag
	for _, tag := range managed {
		if !msg.Props.Has(tag) {
			absent = append(absent, tag)
		}
	}
	if err := st.ModifyMessageProperties(id, msg.Props, absent...); err != nil {
		return err
	}
	return st.ReplaceRecipients(id, msg.Recipients)
}

// patchMaster rewrites the series master, the VEVENT without a RECURRENCE-ID:
// each property the patch names is replaced by its line, or removed when the line
// is "". The overrides and EXDATE lines are left out; CarryExceptions restores
// them when the edit keeps the pattern they name instances of.
func patchMaster(ical []byte, patch map[string]string) []byte {
	var out, block []string
	inEvent := false
	for _, l := range oxcical.ContentLines(ical) {
		switch {
		case strings.EqualFold(l, "BEGIN:VEVENT"):
			inEvent, block = true, []string{l}
		case strings.EqualFold(l, "END:VEVENT") && inEvent:
			inEvent = false
			out = append(out, patchEvent(append(block, l), patch)...)
		case inEvent:
			block = append(block, l)
		default:
			out = append(out, l)
		}
	}
	return []byte(strings.Join(out, "\r\n") + "\r\n")
}

// patchEvent applies the patch to one VEVENT block, nil for an override. Only the
// event's own properties are touched, never those of a VALARM inside it.
func patchEvent(block []string, patch map[string]string) []string {
	out := []string{block[0]}
	depth := 0
	for _, l := range block[1 : len(block)-1] {
		name, _, _ := oxcical.SplitContentLine(l)
		name = strings.ToUpper(name)
		switch {
		case name == "BEGIN":
			depth++
		case name == "END":
			depth--
		case depth > 0:
		case name == "RECURRENCE-ID":
			return nil
		case name == "EXDATE":
			continue
		default:
			if _, ok := patch[name]; ok {
				continue
			}
		}
		out = append(out, l)
	}
	for _, line := range patch {
		if line != "" {
			out = append(out, line)
		}
	}
	return append(out, block[len(block)-1])
}
