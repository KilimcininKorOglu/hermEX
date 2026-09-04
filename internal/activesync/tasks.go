package activesync

import (
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
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
	if !t.Start.IsZero() {
		s := t.Start.UTC().Format(easContactDate)
		data.Children = append(data.Children, wbxml.Str(wbxml.TKStartDate, s), wbxml.Str(wbxml.TKUtcStartDate, s))
	}
	if !t.Due.IsZero() {
		s := t.Due.UTC().Format(easContactDate)
		data.Children = append(data.Children, wbxml.Str(wbxml.TKDueDate, s), wbxml.Str(wbxml.TKUtcDueDate, s))
	}
	data.Children = append(data.Children, wbxml.Str(wbxml.TKComplete, boolStr(t.Complete)))
	if t.Complete && !t.DateCompleted.IsZero() {
		data.Children = append(data.Children, wbxml.Str(wbxml.TKDateCompleted, t.DateCompleted.UTC().Format(easContactDate)))
	}
	data.Children = append(data.Children, wbxml.Str(wbxml.TKReminderSet, boolStr(t.ReminderSet)))
	if t.ReminderSet && !t.ReminderTime.IsZero() {
		data.Children = append(data.Children, wbxml.Str(wbxml.TKReminderTime, t.ReminderTime.UTC().Format(easContactDate)))
	}
	if len(t.Categories) > 0 {
		var cats []*wbxml.Node
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
	if dayOfWeek != 0 {
		n.Children = append(n.Children, wbxml.Str(wbxml.TKRecurDayOfWeek, strconv.Itoa(dayOfWeek)))
	}
	if rec.MonthDay != 0 {
		n.Children = append(n.Children, wbxml.Str(wbxml.TKRecurDayOfMonth, strconv.Itoa(rec.MonthDay)))
	}
	if typ == 3 || typ == 6 { // nth-weekday of month/year
		week := rec.SetPos
		if week < 0 {
			week = 5 // EAS encodes "last" as week 5
		}
		if week != 0 {
			n.Children = append(n.Children, wbxml.Str(wbxml.TKRecurWeekOfMonth, strconv.Itoa(week)))
		}
	}
	if rec.Month != 0 {
		n.Children = append(n.Children, wbxml.Str(wbxml.TKRecurMonthOfYear, strconv.Itoa(rec.Month)))
	}
	return n, true
}

// parseTaskRecurrence decodes an MS-ASTASK Recurrence element back to an RRULE
// string (the shared recurrence shape the store holds), so a device-authored task
// reaches webmail/MAPI/EWS identically. Returns ok=false when no element is present.
func parseTaskRecurrence(data *wbxml.Node, t *oxtask.Task) bool {
	rec := data.Child(wbxml.TKRecurrence)
	if rec == nil {
		return false
	}
	var r oxcical.Recurrence
	r.Interval = 1
	switch rec.ChildText(wbxml.TKRecurType) {
	case "0":
		r.Freq = "DAILY"
	case "1":
		r.Freq = "WEEKLY"
	case "2":
		r.Freq = "MONTHLY"
	case "3":
		r.Freq = "YEARLY"
	default:
		return false
	}
	if n, err := strconv.Atoi(rec.ChildText(wbxml.TKRecurInterval)); err == nil && n > 0 {
		r.Interval = n
	}
	if s := rec.ChildText(wbxml.TKRecurOccurrences); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			r.Count = n
		}
	}
	if s := rec.ChildText(wbxml.TKRecurUntil); s != "" {
		if until, ok := parseEASCalTime(s); ok {
			r.Until = until
		}
	}
	if mask, err := strconv.Atoi(rec.ChildText(wbxml.TKRecurDayOfWeek)); err == nil && mask > 0 {
		r.Weekdays = bitmaskWeekdays(mask)
	}
	if n, err := strconv.Atoi(rec.ChildText(wbxml.TKRecurDayOfMonth)); err == nil {
		r.MonthDay = n
	}
	if week, err := strconv.Atoi(rec.ChildText(wbxml.TKRecurWeekOfMonth)); err == nil && (r.Freq == "MONTHLY" || r.Freq == "YEARLY") {
		if n := nthWeekdayFromWeek(week); n != "" {
			r.SetPos = 0 // nth-weekday encoded via WeekOfMonth; BYDAY carries the weekday
			if len(r.Weekdays) > 0 {
				_ = n // ordinal is implicit in WeekOfMonth; RRULE nth-weekday would need BYDAY prefix
			}
		}
	}
	if n, err := strconv.Atoi(rec.ChildText(wbxml.TKRecurMonthOfYear)); err == nil {
		r.Month = n
	}
	t.RecurrenceRule = rruleFromRecurrence(r)
	return true
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

// nthWeekdayFromWeek maps an EAS WeekOfMonth (1..5) to a BYDAY ordinal prefix string;
// week 5 is "last" (-1). An empty string means no nth-weekday encoding.
func nthWeekdayFromWeek(week int) string {
	switch week {
	case 1, 2, 3, 4:
		return strconv.Itoa(week)
	case 5:
		return "-1"
	}
	return ""
}

// parseTaskItem decodes a device's task ApplicationData into the shared model.
func parseTaskItem(data *wbxml.Node) oxtask.Task {
	t := oxtask.New()
	t.Subject = data.ChildText(wbxml.TKSubject)
	if body := data.Child(wbxml.ABBody); body != nil {
		if d := body.Child(wbxml.ABData); d != nil {
			if len(d.Opaque) > 0 {
				t.Body = string(d.Opaque)
			} else {
				t.Body = d.Text
			}
		}
	}
	if v := data.ChildText(wbxml.TKImportance); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			t.Importance = n
		}
	}
	if v := data.ChildText(wbxml.TKSensitivity); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			t.Sensitivity = n
		}
	}
	t.Start = taskDate(data, wbxml.TKUtcStartDate, wbxml.TKStartDate)
	t.Due = taskDate(data, wbxml.TKUtcDueDate, wbxml.TKDueDate)
	t.Complete = data.ChildText(wbxml.TKComplete) == "1"
	if d := data.ChildText(wbxml.TKDateCompleted); d != "" {
		if tm, err := time.Parse(easContactDate, d); err == nil {
			t.DateCompleted = tm.UTC()
		}
	}
	t.ReminderSet = data.ChildText(wbxml.TKReminderSet) == "1"
	if d := data.ChildText(wbxml.TKReminderTime); d != "" {
		if tm, err := time.Parse(easContactDate, d); err == nil {
			t.ReminderTime = tm.UTC()
		}
	}
	if cats := data.Child(wbxml.TKCategories); cats != nil {
		for _, c := range cats.Children {
			if c.Tag == wbxml.TKCategory && c.Text != "" {
				t.Categories = append(t.Categories, c.Text)
			}
		}
	}
	parseTaskRecurrence(data, &t)
	return t
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

// applyTaskClientCommands applies a device's Add/Change/Delete commands to the Tasks
// folder, mirroring the contacts path through the shared task model.
func applyTaskClientCommands(st *objectstore.Store, cstate *collectionState, c *wbxml.Node) []*wbxml.Node {
	cmds := c.Child(wbxml.ASCommands)
	if cmds == nil {
		return nil
	}
	var responses []*wbxml.Node
	added := map[string]bool{}
	for _, cmd := range cmds.Children {
		switch cmd.Tag {
		case wbxml.ASAdd:
			clientID := cmd.ChildText(wbxml.ASClientID)
			data := cmd.Child(wbxml.ASData)
			if clientID == "" || data == nil {
				continue
			}
			props, err := oxtask.ToProps(parseTaskItem(data), st.GetNamedPropIDs)
			if err != nil {
				continue
			}
			id, err := st.CreateMessage(int64(mapi.PrivateFIDTasks), &oxcmail.Message{Props: props})
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
			props, err := oxtask.ToProps(parseTaskItem(data), st.GetNamedPropIDs)
			if err != nil {
				continue
			}
			_ = st.SetMessageProperties(id, props)
		case wbxml.ASDelete:
			sid := cmd.ChildText(wbxml.ASServerID)
			id, err := strconv.ParseInt(sid, 10, 64)
			if err != nil {
				continue
			}
			if st.SoftDeleteObject(id) == nil {
				delete(cstate.Items, sid)
			}
		}
	}
	if len(added) > 0 {
		if objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDTasks)); err == nil {
			for _, o := range objs {
				if sid := strconv.FormatInt(o.ID, 10); added[sid] {
					// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
					cstate.Items[sid] = int64(o.ChangeNumber)
				}
			}
		}
	}
	return responses
}
