package activesync

import (
	"encoding/base64"
	"sort"
	"strconv"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/wbxml"
)

// utcTimezone is the MS-ASCAL Timezone field for an appointment whose times are
// stored in UTC: a base64 TIME_ZONE_INFORMATION (172 bytes) with a zero bias and
// no daylight rule, so the UTC StartTime/EndTime carry no further adjustment.
var utcTimezone = base64.StdEncoding.EncodeToString(make([]byte, 172))

// easCalTime formats a UTC instant as MS-ASCAL's compact appointment time,
// YYYYMMDDThhmmssZ.
func easCalTime(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// calendarAppData builds the ActiveSync ApplicationData for one stored appointment
// (MS-ASCAL): its subject, start/end (UTC), location, all-day flag, busy status,
// and modification time stamp, read from the calendar named properties. Times ride
// in a UTC timezone, so the stored UTC instants need no conversion. Recurrence and
// attendees are later increments. It returns nil when the object lacks the
// start/end that make it an appointment (the calendar folder may hold none).
func calendarAppData(st *objectstore.Store, objectID int64) (*wbxml.Node, error) {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole, // 0
		mapi.NameAppointmentEndWhole,   // 1
		mapi.NameBusyStatus,            // 2
		mapi.NameAppointmentLocation,   // 3
		mapi.NameAppointmentSubType,    // 4
	})
	if err != nil {
		return nil, err
	}
	if ids[0] == 0 || ids[1] == 0 {
		return nil, nil // the mailbox has never stored an appointment
	}
	startTag := mapi.MakeTag(ids[0], mapi.PtSysTime)
	endTag := mapi.MakeTag(ids[1], mapi.PtSysTime)
	busyTag := mapi.MakeTag(ids[2], mapi.PtLong)
	locTag := mapi.MakeTag(ids[3], mapi.PtUnicode)
	allDayTag := mapi.MakeTag(ids[4], mapi.PtBoolean)

	pv, err := st.GetMessageProperties(objectID, startTag, endTag, busyTag, locTag, allDayTag,
		mapi.PrSubject, mapi.PrLastModificationTime, mapi.PrIcalOriginal)
	if err != nil {
		return nil, err
	}
	start, ok := ntTimeProp(pv, startTag)
	if !ok {
		return nil, nil // no start: not an appointment
	}
	end, hasEnd := ntTimeProp(pv, endTag)

	// A recurring appointment stores only its start named property plus the
	// verbatim iCal; its end and recurrence pattern come from there.
	var recurrence *wbxml.Node
	if ical, ok := bytesProp(pv, mapi.PrIcalOriginal); ok && len(ical) > 0 {
		if s, e, r, ok := oxcical.ParseRecurrence(ical); ok {
			start, end, hasEnd = s, e, true
			recurrence = easRecurrence(r)
		}
	}
	if !hasEnd {
		return nil, nil
	}
	stamp := start
	if mod, ok := ntTimeProp(pv, mapi.PrLastModificationTime); ok {
		stamp = mod
	}

	data := wbxml.Elem(wbxml.ASData,
		wbxml.Str(wbxml.CalTimezone, utcTimezone),
		wbxml.Str(wbxml.CalDtStamp, easCalTime(stamp)),
		wbxml.Str(wbxml.CalStartTime, easCalTime(start)),
		wbxml.Str(wbxml.CalSubject, stringProp(pv, mapi.PrSubject)),
		wbxml.Str(wbxml.CalEndTime, easCalTime(end)),
		wbxml.Str(wbxml.CalBusyStatus, strconv.Itoa(int(longProp(pv, busyTag)))),
		wbxml.Str(wbxml.CalAllDayEvent, boolStr(boolProp(pv, allDayTag))),
	)
	if recurrence != nil {
		data.Children = append(data.Children, recurrence)
	}
	// No attendees are emitted yet, so the appointment is not a meeting.
	data.Children = append(data.Children, wbxml.Str(wbxml.CalMeetingStatus, "0"))
	if loc := stringProp(pv, locTag); loc != "" {
		data.Children = append(data.Children, wbxml.Str(wbxml.CalLocation, loc))
	}
	return data, nil
}

// easRecurrence renders a parsed recurrence as the MS-ASCAL Recurrence element.
// The end bound is at most one of Until (an instant) or Occurrences (a count);
// the by-rules attach only for the recurrence types that use them.
func easRecurrence(rec oxcical.Recurrence) *wbxml.Node {
	typ, dayOfWeek := recurrenceType(rec)
	n := wbxml.Elem(wbxml.CalRecurrence, wbxml.Str(wbxml.CalType, strconv.Itoa(typ)))
	if !rec.Until.IsZero() {
		n.Children = append(n.Children, wbxml.Str(wbxml.CalUntil, easCalTime(rec.Until)))
	} else if rec.Count > 0 {
		n.Children = append(n.Children, wbxml.Str(wbxml.CalOccurrences, strconv.Itoa(rec.Count)))
	}
	n.Children = append(n.Children, wbxml.Str(wbxml.CalInterval, strconv.Itoa(rec.Interval)))
	if dayOfWeek != 0 {
		n.Children = append(n.Children, wbxml.Str(wbxml.CalDayOfWeek, strconv.Itoa(dayOfWeek)))
	}
	if rec.MonthDay != 0 {
		n.Children = append(n.Children, wbxml.Str(wbxml.CalDayOfMonth, strconv.Itoa(rec.MonthDay)))
	}
	if typ == 3 || typ == 6 { // nth-weekday of month/year
		week := rec.SetPos
		if week < 0 {
			week = 5 // EAS encodes "last" as week 5
		}
		if week != 0 {
			n.Children = append(n.Children, wbxml.Str(wbxml.CalWeekOfMonth, strconv.Itoa(week)))
		}
	}
	if rec.Month != 0 {
		n.Children = append(n.Children, wbxml.Str(wbxml.CalMonthOfYear, strconv.Itoa(rec.Month)))
	}
	return n
}

// recurrenceType maps a parsed recurrence to its MS-ASCAL Type (0 daily, 1 weekly,
// 2 monthly-by-day, 3 monthly-nth-weekday, 5 yearly, 6 yearly-nth-weekday) and the
// DayOfWeek bitmask the weekly and nth-weekday types carry.
func recurrenceType(rec oxcical.Recurrence) (typ, dayOfWeek int) {
	dow := weekdayBitmask(rec.Weekdays)
	switch rec.Freq {
	case "DAILY":
		return 0, 0
	case "WEEKLY":
		return 1, dow
	case "MONTHLY":
		if len(rec.Weekdays) > 0 {
			return 3, dow
		}
		return 2, 0
	case "YEARLY":
		if len(rec.Weekdays) > 0 {
			return 6, dow
		}
		return 5, 0
	}
	return 0, 0
}

// weekdayBitmask folds BYDAY weekday tokens into the MS-ASCAL DayOfWeek bitmask
// (Sunday 1, Monday 2, ... Saturday 64).
func weekdayBitmask(days []string) int {
	bits := map[string]int{"SU": 1, "MO": 2, "TU": 4, "WE": 8, "TH": 16, "FR": 32, "SA": 64}
	mask := 0
	for _, d := range days {
		mask |= bits[d]
	}
	return mask
}

// parseEASCalTime parses MS-ASCAL's compact appointment time (YYYYMMDDThhmmssZ).
func parseEASCalTime(s string) (time.Time, bool) {
	if t, err := time.Parse("20060102T150405Z", s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// parseCalendarItem builds the appointment named properties from a device's
// MS-ASCAL ApplicationData (start/end/subject/location/busy-status/all-day).
// Recurrence is not reversed in this increment, so a client edit to a recurring
// series rewrites only its scalar fields.
func parseCalendarItem(st *objectstore.Store, data *wbxml.Node) (mapi.PropertyValues, error) {
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole,
		mapi.NameAppointmentEndWhole,
		mapi.NameBusyStatus,
		mapi.NameAppointmentLocation,
		mapi.NameAppointmentSubType,
	})
	if err != nil {
		return nil, err
	}
	var props mapi.PropertyValues
	if t, ok := parseEASCalTime(data.ChildText(wbxml.CalStartTime)); ok {
		props = append(props, mapi.TaggedPropVal{Tag: mapi.MakeTag(ids[0], mapi.PtSysTime), Value: mapi.UnixToNTTime(t)})
	}
	if t, ok := parseEASCalTime(data.ChildText(wbxml.CalEndTime)); ok {
		props = append(props, mapi.TaggedPropVal{Tag: mapi.MakeTag(ids[1], mapi.PtSysTime), Value: mapi.UnixToNTTime(t)})
	}
	if b := data.ChildText(wbxml.CalBusyStatus); b != "" {
		if n, err := strconv.Atoi(b); err == nil {
			props = append(props, mapi.TaggedPropVal{Tag: mapi.MakeTag(ids[2], mapi.PtLong), Value: int32(n)})
		}
	}
	if loc := data.ChildText(wbxml.CalLocation); loc != "" {
		props = append(props, mapi.TaggedPropVal{Tag: mapi.MakeTag(ids[3], mapi.PtUnicode), Value: loc})
	}
	if ad := data.ChildText(wbxml.CalAllDayEvent); ad != "" {
		props = append(props, mapi.TaggedPropVal{Tag: mapi.MakeTag(ids[4], mapi.PtBoolean), Value: ad == "1"})
	}
	if subj := data.ChildText(wbxml.CalSubject); subj != "" {
		props = append(props, mapi.TaggedPropVal{Tag: mapi.PrSubject, Value: subj})
	}
	return props, nil
}

// applyCalendarClientCommands applies a device's Change and Delete commands to the
// calendar folder. A Change rewrites the appointment's scalar fields without
// bumping the change number (SetMessageProperties), so it is not echoed back to
// the device that made it. Client-side adds (which need a server-id mapping) and
// recurrence edits are later increments.
func applyCalendarClientCommands(st *objectstore.Store, cstate *collectionState, c *wbxml.Node) []*wbxml.Node {
	cmds := c.Child(wbxml.ASCommands)
	if cmds == nil {
		return nil
	}
	var responses []*wbxml.Node
	added := map[string]bool{}
	for _, cmd := range cmds.Children {
		switch cmd.Tag {
		case wbxml.ASAdd:
			// A device add carries a client id (not a server id); the server
			// creates the appointment and returns the id it assigned.
			clientID := cmd.ChildText(wbxml.ASClientID)
			data := cmd.Child(wbxml.ASData)
			if clientID == "" || data == nil {
				continue
			}
			props, err := parseCalendarItem(st, data)
			if err != nil {
				continue
			}
			props = append(props, mapi.TaggedPropVal{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"})
			id, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: props})
			if err != nil {
				continue
			}
			sid := strconv.FormatInt(id, 10)
			added[sid] = true
			responses = append(responses, wbxml.Elem(wbxml.ASAdd,
				wbxml.Str(wbxml.ASClientID, clientID),
				wbxml.Str(wbxml.ASServerID, sid),
				wbxml.Str(wbxml.ASStatus, strconv.Itoa(syncStatusOK))))
		case wbxml.ASChange:
			id, err := strconv.ParseInt(cmd.ChildText(wbxml.ASServerID), 10, 64)
			if err != nil {
				continue
			}
			data := cmd.Child(wbxml.ASData)
			if data == nil {
				continue
			}
			props, err := parseCalendarItem(st, data)
			if err != nil || len(props) == 0 {
				continue
			}
			_ = st.SetMessageProperties(id, props)
		case wbxml.ASDelete:
			sid := cmd.ChildText(wbxml.ASServerID)
			id, err := strconv.ParseInt(sid, 10, 64)
			if err != nil {
				continue
			}
			if st.DeleteObject(id) == nil {
				delete(cstate.Items, sid)
			}
		}
	}
	// Fold the just-added appointments into the snapshot so calendarChanges does
	// not echo them back as server adds to the device that just created them.
	if len(added) > 0 {
		if objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar)); err == nil {
			for _, o := range objs {
				if sid := strconv.FormatInt(o.ID, 10); added[sid] {
					cstate.Items[sid] = int64(o.ChangeNumber)
				}
			}
		}
	}
	return responses
}

// bytesProp reads a PtBinary property as raw bytes.
func bytesProp(pv mapi.PropertyValues, tag mapi.PropTag) ([]byte, bool) {
	if v, ok := pv.Get(tag); ok {
		if b, ok := v.([]byte); ok {
			return b, true
		}
	}
	return nil, false
}

// ntTimeProp reads a PtSysTime property (stored as an NT-time uint64) as a UTC
// instant; tag 0 or an absent/!uint64 value reports not-present.
func ntTimeProp(pv mapi.PropertyValues, tag mapi.PropTag) (time.Time, bool) {
	if tag == 0 {
		return time.Time{}, false
	}
	if v, ok := pv.Get(tag); ok {
		if nt, ok := v.(uint64); ok {
			return mapi.NTTimeToUnix(nt).UTC(), true
		}
	}
	return time.Time{}, false
}

func longProp(pv mapi.PropertyValues, tag mapi.PropTag) int32 {
	if v, ok := pv.Get(tag); ok {
		if n, ok := v.(int32); ok {
			return n
		}
	}
	return 0
}

func boolProp(pv mapi.PropertyValues, tag mapi.PropTag) bool {
	if v, ok := pv.Get(tag); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func stringProp(pv mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := pv.Get(tag); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// calendarChanges diffs the calendar folder's stored appointments against the
// device snapshot — keyed by object id -> change number, since calendar items
// carry no IMAP flags — and builds the Add/Change/Delete commands, capped at the
// window. A new id is an Add, a bumped change number a Change, a vanished id a
// Delete; the snapshot records the change number of every item it sends, so a
// capped-out item is re-detected on the next sync.
func calendarChanges(st *objectstore.Store, folderID int64, cstate *collectionState, window int) ([]*wbxml.Node, bool, error) {
	objs, err := st.ListFolderObjects(folderID)
	if err != nil {
		return nil, false, err
	}
	type change struct {
		kind int
		sid  string
		id   int64
		cn   int64
	}
	var pending []change
	live := make(map[string]bool, len(objs))
	for _, o := range objs {
		sid := strconv.FormatInt(o.ID, 10)
		live[sid] = true
		switch prev, ok := cstate.Items[sid]; {
		case !ok:
			pending = append(pending, change{changeAdd, sid, o.ID, int64(o.ChangeNumber)})
		case prev != int64(o.ChangeNumber):
			pending = append(pending, change{changeChange, sid, o.ID, int64(o.ChangeNumber)})
		}
	}
	var deletes []string
	for sid := range cstate.Items {
		if !live[sid] {
			deletes = append(deletes, sid)
		}
	}
	sort.Slice(deletes, func(i, j int) bool { return lessSID(deletes[i], deletes[j]) })
	for _, sid := range deletes {
		pending = append(pending, change{kind: changeDelete, sid: sid})
	}

	more := false
	if len(pending) > window {
		pending = pending[:window]
		more = true
	}

	var cmds []*wbxml.Node
	for _, ch := range pending {
		switch ch.kind {
		case changeAdd, changeChange:
			data, err := calendarAppData(st, ch.id)
			if err != nil {
				return nil, false, err
			}
			if data == nil {
				continue // not an appointment; nothing to stream
			}
			tag := wbxml.ASAdd
			if ch.kind == changeChange {
				tag = wbxml.ASChange
			}
			cmds = append(cmds, wbxml.Elem(tag, wbxml.Str(wbxml.ASServerID, ch.sid), data))
			cstate.Items[ch.sid] = ch.cn
		case changeDelete:
			cmds = append(cmds, wbxml.Elem(wbxml.ASDelete, wbxml.Str(wbxml.ASServerID, ch.sid)))
			delete(cstate.Items, ch.sid)
		}
	}
	return cmds, more, nil
}
