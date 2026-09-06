package activesync

import (
	"encoding/base64"
	"sort"
	"strconv"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
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
	tags, ok, err := calendarTags(st)
	if err != nil || !ok {
		return nil, err
	}
	pv, err := st.GetMessageProperties(objectID, tags.start, tags.end, tags.busy, tags.loc, tags.allDay,
		mapi.PrSubject, mapi.PrLastModificationTime, mapi.PrIcalOriginal)
	if err != nil {
		return nil, err
	}
	start, end, recurrence, ok := calendarSpan(pv, tags)
	if !ok {
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
		wbxml.Str(wbxml.CalBusyStatus, strconv.Itoa(int(longProp(pv, tags.busy)))),
		wbxml.Str(wbxml.CalAllDayEvent, boolStr(boolProp(pv, tags.allDay))),
	)
	if recurrence != nil {
		data.Children = append(data.Children, recurrence)
	}
	// No attendees are emitted yet, so the appointment is not a meeting.
	data.Children = append(data.Children, wbxml.Str(wbxml.CalMeetingStatus, "0"))
	if loc := stringProp(pv, tags.loc); loc != "" {
		data.Children = append(data.Children, wbxml.Str(wbxml.CalLocation, loc))
	}
	return data, nil
}

// calendarTagSet holds the appointment named-property tags one mailbox resolved.
type calendarTagSet struct {
	start, end, busy, loc, allDay mapi.PropTag
}

// calendarTags resolves the appointment named properties. ok is false when the
// mailbox has never stored an appointment, so nothing can be rendered from it.
func calendarTags(st *objectstore.Store) (calendarTagSet, bool, error) {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole, // 0
		mapi.NameAppointmentEndWhole,   // 1
		mapi.NameBusyStatus,            // 2
		mapi.NameAppointmentLocation,   // 3
		mapi.NameAppointmentSubType,    // 4
	})
	if err != nil {
		return calendarTagSet{}, false, err
	}
	if ids[0] == 0 || ids[1] == 0 {
		return calendarTagSet{}, false, nil
	}
	return calendarTagSet{
		start:  mapi.MakeTag(ids[0], mapi.PtSysTime),
		end:    mapi.MakeTag(ids[1], mapi.PtSysTime),
		busy:   mapi.MakeTag(ids[2], mapi.PtLong),
		loc:    mapi.MakeTag(ids[3], mapi.PtUnicode),
		allDay: mapi.MakeTag(ids[4], mapi.PtBoolean),
	}, true, nil
}

// calendarSpan resolves one object's start, end and recurrence. A recurring
// appointment stores only its start named property plus the verbatim iCal, so its
// end and recurrence pattern come from there. ok is false when the object carries
// no start or end, which means it is not an appointment.
func calendarSpan(pv mapi.PropertyValues, tags calendarTagSet) (start, end time.Time, recurrence *wbxml.Node, ok bool) {
	start, ok = ntTimeProp(pv, tags.start)
	if !ok {
		return time.Time{}, time.Time{}, nil, false
	}
	end, hasEnd := ntTimeProp(pv, tags.end)
	if ical, ok := bytesProp(pv, mapi.PrIcalOriginal); ok && len(ical) > 0 {
		if s, e, r, ok := oxcical.ParseRecurrence(ical); ok {
			start, end, hasEnd = s, e, true
			recurrence = easRecurrence(r)
		}
	}
	if !hasEnd {
		return time.Time{}, time.Time{}, nil, false
	}
	return start, end, recurrence, true
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
	appendNonZero(n, wbxml.CalDayOfWeek, dayOfWeek)
	appendNonZero(n, wbxml.CalDayOfMonth, rec.MonthDay)
	if typ == 3 || typ == 6 { // nth-weekday of month/year
		appendNonZero(n, wbxml.CalWeekOfMonth, easWeekOfMonth(rec.SetPos))
	}
	appendNonZero(n, wbxml.CalMonthOfYear, rec.Month)
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
		// PtLong is 32-bit, and the value comes off the wire: parse at that width so
		// an out-of-range status is rejected rather than wrapped into a valid one.
		if n, err := strconv.ParseInt(b, 10, 32); err == nil {
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

// parseCalendarProps decodes a device's appointment ApplicationData and stamps
// the message class the store files an appointment under.
func parseCalendarProps(st *objectstore.Store, data *wbxml.Node) (mapi.PropertyValues, error) {
	props, err := parseCalendarItem(st, data)
	if err != nil {
		return nil, err
	}
	return append(props, mapi.TaggedPropVal{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"}), nil
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

// objectChanges diffs an object folder's stored items (calendar appointments or
// contacts) against the device snapshot, keyed by object id -> change number, since
// these items carry no IMAP flags, and builds the Add/Change/Delete commands, capped
// at the window. A new id is an Add, a bumped change number a Change, a vanished id a
// Delete; the snapshot records the change number of every item it sends, so a
// capped-out item is re-detected on the next sync. appData renders one item's
// ApplicationData for the folder's data class.
func objectChanges(st *objectstore.Store, folderID int64, cstate *collectionState, window int, appData func(*objectstore.Store, int64) (*wbxml.Node, error)) ([]*wbxml.Node, bool, error) {
	objs, err := st.ListFolderObjects(folderID)
	if err != nil {
		return nil, false, err
	}
	pending := pendingObjectChanges(objs, cstate)
	more := false
	if len(pending) > window {
		pending = pending[:window]
		more = true
	}

	var cmds []*wbxml.Node
	for _, ch := range pending {
		cmd, err := renderObjectChange(st, cstate, ch, appData)
		if err != nil {
			return nil, false, err
		}
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return cmds, more, nil
}

// objectChange is one pending Add, Change or Delete for an object folder's item.
type objectChange struct {
	kind int
	sid  string
	id   int64
	cn   int64
}

// pendingObjectChanges diffs the folder's stored items against the device
// snapshot: a new id is an Add, a bumped change number a Change, and an id the
// snapshot holds but the folder no longer does a Delete.
func pendingObjectChanges(objs []objectstore.FolderObject, cstate *collectionState) []objectChange {
	var pending []objectChange
	live := make(map[string]bool, len(objs))
	for _, o := range objs {
		sid := strconv.FormatInt(o.ID, 10)
		live[sid] = true
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		cn := int64(o.ChangeNumber)
		switch prev, ok := cstate.Items[sid]; {
		case !ok:
			pending = append(pending, objectChange{changeAdd, sid, o.ID, cn})
		case prev != cn:
			pending = append(pending, objectChange{changeChange, sid, o.ID, cn})
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
		pending = append(pending, objectChange{kind: changeDelete, sid: sid})
	}
	return pending
}

// renderObjectChange builds one change's command and records it in the snapshot.
// It answers a nil command for an item the data class cannot render, which is
// nothing to stream.
func renderObjectChange(st *objectstore.Store, cstate *collectionState, ch objectChange,
	appData func(*objectstore.Store, int64) (*wbxml.Node, error)) (*wbxml.Node, error) {
	if ch.kind == changeDelete {
		delete(cstate.Items, ch.sid)
		return wbxml.Elem(wbxml.ASDelete, wbxml.Str(wbxml.ASServerID, ch.sid)), nil
	}
	data, err := appData(st, ch.id)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	tag := wbxml.ASAdd
	if ch.kind == changeChange {
		tag = wbxml.ASChange
	}
	cstate.Items[ch.sid] = ch.cn
	return wbxml.Elem(tag, wbxml.Str(wbxml.ASServerID, ch.sid), data), nil
}
