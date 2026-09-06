package activesync

import (
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxtask"
	"hermex/internal/wbxml"
)

// MS-ASTASK: a stored IPM.Task maps to and from the AirSync Tasks class through the
// shared oxtask model, so a task is the same object across webmail, ActiveSync, EWS,
// and a MAPI client. The recurrence is stored as an RRULE string (oxtask.RecurrenceRule,
// shared across protocols) and rendered to/from the MS-ASTASK Recurrence element here.

// taskAppData builds the AirSync ApplicationData for a stored task.
func taskAppData(st *objectstore.Store, objectID int64) (*wbxml.Node, error) {
	msg, err := st.OpenMessage(objectID)
	if err != nil {
		return nil, err
	}
	if contactStr(msg.Props, mapi.PrMessageClass) != oxtask.MessageClass {
		return nil, nil // not a task; nothing to stream
	}
	t, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
	if err != nil {
		return nil, err
	}
	data := wbxml.Elem(wbxml.ASData)
	appendTaskText(data, t)
	appendTaskDates(data, t)
	appendTaskFlag(data, wbxml.TKComplete, t.Complete, wbxml.TKDateCompleted, t.DateCompleted)
	appendTaskFlag(data, wbxml.TKReminderSet, t.ReminderSet, wbxml.TKReminderTime, t.ReminderTime)
	if len(t.Categories) > 0 {
		cats := make([]*wbxml.Node, 0, len(t.Categories))
		for _, c := range t.Categories {
			cats = append(cats, wbxml.Str(wbxml.TKCategory, c))
		}
		data.Children = append(data.Children, wbxml.Elem(wbxml.TKCategories, cats...))
	}
	if rec, ok := taskRecurrence(t); ok {
		data.Children = append(data.Children, rec)
	}
	return data, nil
}

// appendTaskFlag emits one boolean task field, followed by the timestamp that
// belongs to it when the flag is set and the timestamp is present.
func appendTaskFlag(data *wbxml.Node, flagTag wbxml.Tag, set bool, timeTag wbxml.Tag, when time.Time) {
	data.Children = append(data.Children, wbxml.Str(flagTag, boolStr(set)))
	if set && !when.IsZero() {
		data.Children = append(data.Children, wbxml.Str(timeTag, when.UTC().Format(easContactDate)))
	}
}

// appendTaskText emits the subject, the body and the two rating fields.
func appendTaskText(data *wbxml.Node, t oxtask.Task) {
	if t.Subject != "" {
		data.Children = append(data.Children, wbxml.Str(wbxml.TKSubject, t.Subject))
	}
	if t.Body != "" {
		data.Children = append(data.Children, wbxml.Elem(wbxml.ABBody,
			wbxml.Str(wbxml.ABType, "1"),
			wbxml.Str(wbxml.ABEstimatedDataSize, strconv.Itoa(len(t.Body))),
			wbxml.Opaque(wbxml.ABData, []byte(t.Body))))
	}
	if t.Importance >= 0 {
		data.Children = append(data.Children, wbxml.Str(wbxml.TKImportance, strconv.Itoa(t.Importance)))
	}
	if t.Sensitivity >= 0 {
		data.Children = append(data.Children, wbxml.Str(wbxml.TKSensitivity, strconv.Itoa(t.Sensitivity)))
	}
}

// appendTaskDates emits the start and due dates, each in its local and UTC form.
func appendTaskDates(data *wbxml.Node, t oxtask.Task) {
	if !t.Start.IsZero() {
		s := t.Start.UTC().Format(easContactDate)
		data.Children = append(data.Children, wbxml.Str(wbxml.TKStartDate, s), wbxml.Str(wbxml.TKUtcStartDate, s))
	}
	if !t.Due.IsZero() {
		s := t.Due.UTC().Format(easContactDate)
		data.Children = append(data.Children, wbxml.Str(wbxml.TKDueDate, s), wbxml.Str(wbxml.TKUtcDueDate, s))
	}
}

// taskRecurrence renders the task's RRULE as an MS-ASTASK Recurrence element, the
// Tasks code-page sibling of the MS-ASCAL Recurrence element calendar.go renders.
// The Type/DayOfWeek/WeekOfMonth/MonthOfYear semantics are shared with MS-ASCAL
// (Type 0 daily, 1 weekly, 2 monthly-by-day, 3 monthly-nth-weekday, 5 yearly-by-day,
// 6 yearly-nth-weekday); the recurrence is carried as RRULE text in the store, so a
// task authored in webmail reaches a device verbatim. ok is false when the task has
// no recurrence or the RRULE does not parse.
func taskRecurrence(t oxtask.Task) (*wbxml.Node, bool) {
	if t.RecurrenceRule == "" {
		return nil, false
	}
	rec, ok := oxcical.ParseRRule(t.RecurrenceRule)
	if !ok {
		return nil, false
	}
	typ, dayOfWeek := recurrenceType(rec)
	n := wbxml.Elem(wbxml.TKRecurrence, wbxml.Str(wbxml.TKRecurType, strconv.Itoa(typ)))
	// RecurStart is the first instance; the task's own start is the series anchor.
	if !t.Start.IsZero() {
		n.Children = append(n.Children, wbxml.Str(wbxml.TKRecurStart, t.Start.UTC().Format(easContactDate)))
	}
	if !rec.Until.IsZero() {
		n.Children = append(n.Children, wbxml.Str(wbxml.TKRecurUntil, easCalTime(rec.Until)))
	} else if rec.Count > 0 {
		n.Children = append(n.Children, wbxml.Str(wbxml.TKRecurOccurrences, strconv.Itoa(rec.Count)))
	}
	n.Children = append(n.Children, wbxml.Str(wbxml.TKRecurInterval, strconv.Itoa(rec.Interval)))
	appendNonZero(n, wbxml.TKRecurDayOfWeek, dayOfWeek)
	appendNonZero(n, wbxml.TKRecurDayOfMonth, rec.MonthDay)
	if typ == 3 || typ == 6 { // nth-weekday of month/year
		appendNonZero(n, wbxml.TKRecurWeekOfMonth, easWeekOfMonth(rec.SetPos))
	}
	appendNonZero(n, wbxml.TKRecurMonthOfYear, rec.Month)
	return n, true
}

// appendNonZero emits one numeric recurrence field, skipping a zero value.
func appendNonZero(n *wbxml.Node, tag wbxml.Tag, v int) {
	if v != 0 {
		n.Children = append(n.Children, wbxml.Str(tag, strconv.Itoa(v)))
	}
}

// easWeekOfMonth maps an RRULE BYSETPOS to the EAS WeekOfMonth, which encodes
// "last" as week 5.
func easWeekOfMonth(setPos int) int {
	if setPos < 0 {
		return 5
	}
	return setPos
}

// parseTaskRecurrence decodes an MS-ASTASK Recurrence element back to an RRULE
// string (the shared recurrence shape the store holds), so a device-authored task
// reaches webmail/MAPI/EWS identically. Returns ok=false when no element is present.
func parseTaskRecurrence(data *wbxml.Node, t *oxtask.Task) bool {
	rec := data.Child(wbxml.TKRecurrence)
	if rec == nil {
		return false
	}
	freq, ok := easRecurFreq[rec.ChildText(wbxml.TKRecurType)]
	if !ok {
		return false
	}
	r := oxcical.Recurrence{Freq: freq, Interval: 1}
	setPositive(rec, wbxml.TKRecurInterval, &r.Interval)
	setPositive(rec, wbxml.TKRecurOccurrences, &r.Count)
	setTaskInt(rec, wbxml.TKRecurDayOfMonth, &r.MonthDay)
	setTaskInt(rec, wbxml.TKRecurMonthOfYear, &r.Month)
	if until, ok := parseEASCalTime(rec.ChildText(wbxml.TKRecurUntil)); ok {
		r.Until = until
	}
	var mask int
	if setPositive(rec, wbxml.TKRecurDayOfWeek, &mask); mask > 0 {
		r.Weekdays = bitmaskWeekdays(mask)
	}
	// The nth-weekday ordinal stays implicit in the EAS WeekOfMonth field: BYDAY
	// carries the weekday alone, so SetPos remains zero and rruleFromRecurrence
	// emits no ordinal prefix.
	t.RecurrenceRule = rruleFromRecurrence(r)
	return true
}

// setPositive reads one recurrence count field, keeping the current value when
// the device sent nothing, a non-number, or a value below one.
func setPositive(rec *wbxml.Node, tag wbxml.Tag, dst *int) {
	if n, err := strconv.Atoi(rec.ChildText(tag)); err == nil && n > 0 {
		*dst = n
	}
}

// easRecurFreq maps the MS-ASTASK RecurrenceType to the RRULE frequency. A type
// this table does not carry is not a recurrence this server stores.
var easRecurFreq = map[string]string{
	"0": "DAILY",
	"1": "WEEKLY",
	"2": "MONTHLY",
	"3": "YEARLY",
}

// rruleFromRecurrence renders a neutral recurrence back to the RRULE text the store
// holds. It covers the shapes taskRecurrence emits (DAILY/WEEKLY/MONTHLY/YEARLY with
// INTERVAL, COUNT/UNTIL, BYDAY, BYMONTHDAY, BYMONTH); nth-weekday round-trips via
// BYDAY with an ordinal prefix only when SetPos is set, matching how ParseRRule reads it.
func rruleFromRecurrence(r oxcical.Recurrence) string {
	var b strings.Builder
	b.WriteString("FREQ=")
	b.WriteString(r.Freq)
	if r.Interval > 1 {
		b.WriteString(";INTERVAL=")
		b.WriteString(strconv.Itoa(r.Interval))
	}
	if r.Count > 0 {
		b.WriteString(";COUNT=")
		b.WriteString(strconv.Itoa(r.Count))
	}
	if !r.Until.IsZero() {
		b.WriteString(";UNTIL=")
		b.WriteString(r.Until.UTC().Format("20060102T150405Z"))
	}
	if len(r.Weekdays) > 0 {
		b.WriteString(";BYDAY=")
		for i, d := range r.Weekdays {
			if i > 0 {
				b.WriteByte(',')
			}
			if r.SetPos != 0 {
				b.WriteString(strconv.Itoa(r.SetPos))
			}
			b.WriteString(d)
		}
	}
	if r.MonthDay != 0 {
		b.WriteString(";BYMONTHDAY=")
		b.WriteString(strconv.Itoa(r.MonthDay))
	}
	if r.Month != 0 {
		b.WriteString(";BYMONTH=")
		b.WriteString(strconv.Itoa(r.Month))
	}
	return b.String()
}

// bitmaskWeekdays expands an EAS DayOfWeek bitmask (Sunday 1 .. Saturday 64) back to
// the BYDAY weekday tokens ParseRRule yields.
func bitmaskWeekdays(mask int) []string {
	bits := []struct {
		mask int
		tok  string
	}{
		{1, "SU"}, {2, "MO"}, {4, "TU"}, {8, "WE"}, {16, "TH"}, {32, "FR"}, {64, "SA"},
	}
	var out []string
	for _, b := range bits {
		if mask&b.mask != 0 {
			out = append(out, b.tok)
		}
	}
	return out
}

// parseTaskItem decodes a device's task ApplicationData into the shared model.
func parseTaskItem(data *wbxml.Node) oxtask.Task {
	t := oxtask.New()
	t.Subject = data.ChildText(wbxml.TKSubject)
	t.Body = airSyncBody(data)
	setTaskInt(data, wbxml.TKImportance, &t.Importance)
	setTaskInt(data, wbxml.TKSensitivity, &t.Sensitivity)
	t.Start = taskDate(data, wbxml.TKUtcStartDate, wbxml.TKStartDate)
	t.Due = taskDate(data, wbxml.TKUtcDueDate, wbxml.TKDueDate)
	t.Complete = data.ChildText(wbxml.TKComplete) == "1"
	setTaskTime(data, wbxml.TKDateCompleted, &t.DateCompleted)
	t.ReminderSet = data.ChildText(wbxml.TKReminderSet) == "1"
	setTaskTime(data, wbxml.TKReminderTime, &t.ReminderTime)
	t.Categories = listChildText(data, wbxml.TKCategories, wbxml.TKCategory)
	parseTaskRecurrence(data, &t)
	return t
}

// setTaskInt reads one numeric task field, leaving the model's own default when
// the device sent nothing or sent a value that is not a number.
func setTaskInt(data *wbxml.Node, tag wbxml.Tag, dst *int) {
	if n, err := strconv.Atoi(data.ChildText(tag)); err == nil {
		*dst = n
	}
}

// setTaskTime reads one task date in the MS-ASTASK format, leaving the field
// untouched when the device sent nothing or sent an unparsable value.
func setTaskTime(data *wbxml.Node, tag wbxml.Tag, dst *time.Time) {
	if tm, err := time.Parse(easContactDate, data.ChildText(tag)); err == nil {
		*dst = tm.UTC()
	}
}

// taskDate reads a task date, preferring the UTC field and falling back to the local.
func taskDate(data *wbxml.Node, utcTag, localTag wbxml.Tag) time.Time {
	for _, tag := range []wbxml.Tag{utcTag, localTag} {
		if s := data.ChildText(tag); s != "" {
			if tm, err := time.Parse(easContactDate, s); err == nil {
				return tm.UTC()
			}
		}
	}
	return time.Time{}
}

// parseTaskProps decodes a device's task ApplicationData through the shared task
// model, which stamps the task message class itself.
func parseTaskProps(st *objectstore.Store, data *wbxml.Node) (mapi.PropertyValues, error) {
	return oxtask.ToProps(parseTaskItem(data), st.GetNamedPropIDs)
}
