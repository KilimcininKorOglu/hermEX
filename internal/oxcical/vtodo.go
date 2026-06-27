package oxcical

import (
	"strconv"
	"strings"
	"time"

	"hermex/internal/oxtask"
)

// ExportVTODO renders a task as an iCalendar VTODO object (RFC 5545 §3.6.2), so a
// CalDAV tasks client (e.g. Apple Reminders) reads the same task the web, ActiveSync,
// and EWS surfaces serve. uid is the resource's stable identifier; dtstamp marks the
// object timestamp, defaulting to the task's most recent time.
func ExportVTODO(t oxtask.Task, uid string, dtstamp time.Time) []byte {
	b := &builder{}
	b.add("BEGIN:VCALENDAR")
	b.add("VERSION:2.0")
	b.add("PRODID:-//hermEX//CalDAV//EN")
	b.add("BEGIN:VTODO")
	b.line("UID", uid)
	if dtstamp.IsZero() {
		dtstamp = firstNonZeroTime(t.DateCompleted, t.Due, t.Start)
	}
	if !dtstamp.IsZero() {
		b.add("DTSTAMP:" + formatICalUTC(dtstamp))
	}
	if t.Subject != "" {
		b.line("SUMMARY", t.Subject)
	}
	if t.Body != "" {
		b.line("DESCRIPTION", t.Body)
	}
	if !t.Start.IsZero() {
		b.add("DTSTART:" + formatICalUTC(t.Start))
	}
	if !t.Due.IsZero() {
		b.add("DUE:" + formatICalUTC(t.Due))
	}
	if t.Complete {
		b.add("STATUS:COMPLETED")
		b.add("PERCENT-COMPLETE:100")
		if !t.DateCompleted.IsZero() {
			b.add("COMPLETED:" + formatICalUTC(t.DateCompleted))
		}
	} else {
		b.add("STATUS:NEEDS-ACTION")
	}
	if t.Importance >= 0 {
		b.add("PRIORITY:" + strconv.Itoa(icalPriority(t.Importance)))
	}
	if len(t.Categories) > 0 {
		cats := make([]string, len(t.Categories))
		for i, c := range t.Categories {
			cats[i] = escapeValue(c)
		}
		b.add("CATEGORIES:" + strings.Join(cats, ","))
	}
	b.add("END:VTODO")
	b.add("END:VCALENDAR")
	return b.buf.Bytes()
}

// ParseVTODO parses an iCalendar object's VTODO into the shared task model. It returns
// the task, the VTODO UID, and ok=false when the object carries no VTODO. Fields not
// represented in a VTODO (reminder, sensitivity) are left unset; a caller that updates
// an existing task should merge to preserve them.
func ParseVTODO(raw []byte) (oxtask.Task, string, bool) {
	cal, err := parseICal(raw)
	if err != nil {
		return oxtask.Task{}, "", false
	}
	v := cal.sub("VTODO")
	if v == nil {
		return oxtask.Task{}, "", false
	}
	t := oxtask.New()
	t.Subject = v.propText("SUMMARY")
	t.Body = v.propText("DESCRIPTION")
	if l := v.prop("DTSTART"); l != nil {
		if tm, _, ok := parseICalTime(l); ok {
			t.Start = tm.UTC()
		}
	}
	if l := v.prop("DUE"); l != nil {
		if tm, _, ok := parseICalTime(l); ok {
			t.Due = tm.UTC()
		}
	}
	if strings.EqualFold(v.propText("STATUS"), "COMPLETED") || v.propText("PERCENT-COMPLETE") == "100" {
		t.Complete = true
	}
	if l := v.prop("COMPLETED"); l != nil {
		if tm, _, ok := parseICalTime(l); ok {
			t.Complete = true
			t.DateCompleted = tm.UTC()
		}
	}
	if pr := v.propText("PRIORITY"); pr != "" {
		if n, err := strconv.Atoi(pr); err == nil {
			t.Importance = importanceFromICal(n)
		}
	}
	if cats := v.propText("CATEGORIES"); cats != "" {
		for c := range strings.SplitSeq(cats, ",") {
			if c = strings.TrimSpace(c); c != "" {
				t.Categories = append(t.Categories, c)
			}
		}
	}
	return t, strings.TrimSpace(v.propText("UID")), true
}

// firstNonZeroTime returns the first non-zero time, or the zero time.
func firstNonZeroTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// icalPriority maps PR_IMPORTANCE (0 low, 1 normal, 2 high) to an iCalendar PRIORITY
// (1 highest .. 9 lowest).
func icalPriority(importance int) int {
	switch importance {
	case 2:
		return 1
	case 0:
		return 9
	default:
		return 5
	}
}

// importanceFromICal maps an iCalendar PRIORITY (1..9, 0 undefined) back to
// PR_IMPORTANCE.
func importanceFromICal(priority int) int {
	switch {
	case priority == 0:
		return 1
	case priority <= 4:
		return 2
	case priority >= 6:
		return 0
	default:
		return 1
	}
}
