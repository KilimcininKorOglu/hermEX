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
	addLine(b, "SUMMARY", t.Subject)
	addLine(b, "DESCRIPTION", t.Body)
	addTimeLine(b, "DTSTART", t.Start)
	addTimeLine(b, "DUE", t.Due)
	exportTaskStatus(b, t)
	if t.Importance >= 0 {
		b.add("PRIORITY:" + strconv.Itoa(icalPriority(t.Importance)))
	}
	if len(t.Categories) > 0 {
		b.add("CATEGORIES:" + joinEscaped(t.Categories))
	}
	b.add("END:VTODO")
	b.add("END:VCALENDAR")
	return b.buf.Bytes()
}

// addTimeLine emits a UTC date-time property only when the instant is set.
func addTimeLine(b *builder, name string, t time.Time) {
	if !t.IsZero() {
		b.add(name + ":" + formatICalUTC(t))
	}
}

// exportTaskStatus emits the completion state, including when it was finished.
func exportTaskStatus(b *builder, t oxtask.Task) {
	if !t.Complete {
		b.add("STATUS:NEEDS-ACTION")
		return
	}
	b.add("STATUS:COMPLETED")
	b.add("PERCENT-COMPLETE:100")
	addTimeLine(b, "COMPLETED", t.DateCompleted)
}

// joinEscaped renders a category list as one escaped, comma-separated value.
func joinEscaped(values []string) string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = escapeValue(v)
	}
	return strings.Join(out, ",")
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
	if tm, ok := componentTime(v, "DTSTART"); ok {
		t.Start = tm
	}
	if tm, ok := componentTime(v, "DUE"); ok {
		t.Due = tm
	}
	parseTaskStatus(v, &t)
	if n, err := strconv.Atoi(v.propText("PRIORITY")); err == nil {
		t.Importance = importanceFromICal(n)
	}
	t.Categories = splitCategories(v.propText("CATEGORIES"))
	return t, strings.TrimSpace(v.propText("UID")), true
}

// componentTime reads a component's date-time property as a UTC instant.
func componentTime(c *icomp, name string) (time.Time, bool) {
	l := c.prop(name)
	if l == nil {
		return time.Time{}, false
	}
	tm, _, ok := parseICalTime(l)
	if !ok {
		return time.Time{}, false
	}
	return tm.UTC(), true
}

// parseTaskStatus reads the completion state a VTODO reports, which a client may
// state as STATUS, as a percentage, or by carrying a COMPLETED timestamp.
func parseTaskStatus(v *icomp, t *oxtask.Task) {
	if strings.EqualFold(v.propText("STATUS"), "COMPLETED") || v.propText("PERCENT-COMPLETE") == "100" {
		t.Complete = true
	}
	if tm, ok := componentTime(v, "COMPLETED"); ok {
		t.Complete = true
		t.DateCompleted = tm
	}
}

// splitCategories reads a comma-separated CATEGORIES value, dropping empty entries.
func splitCategories(value string) []string {
	var out []string
	for c := range strings.SplitSeq(value, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
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
