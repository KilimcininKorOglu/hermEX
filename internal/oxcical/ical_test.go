package oxcical

import (
	"errors"
	"strings"
	"testing"
)

// compDepth returns the depth of the component tree rooted at c, counting c
// itself as one level.
func compDepth(c *icomp) int {
	deepest := 0
	for _, sub := range c.comps {
		if d := compDepth(sub); d > deepest {
			deepest = d
		}
	}
	return deepest + 1
}

// TestParseICalBoundsNesting confirms that a run of BEGIN lines far past the
// legal nesting of a calendar produces a bounded tree. The parsed tree is walked
// by recursive descent (writeComponent, filterComp), and the bytes reach the
// parser from unauthenticated inbound SMTP, so an unbounded tree is a stack
// overflow that no recover() catches.
func TestParseICalBoundsNesting(t *testing.T) {
	const levels = maxNestingDepth + 500
	src := "BEGIN:VCALENDAR\r\n" +
		strings.Repeat("BEGIN:VEVENT\r\n", levels) +
		"SUMMARY:deep\r\n" +
		strings.Repeat("END:VEVENT\r\n", levels) +
		"END:VCALENDAR\r\n"

	cal, err := parseICal([]byte(src))
	if err != nil {
		t.Fatalf("parseICal: %v", err)
	}
	if got := compDepth(cal); got > maxNestingDepth {
		t.Errorf("tree depth = %d, want at most %d", got, maxNestingDepth)
	}
	// The dropped levels must not unbalance the parse: the calendar root is
	// still the outermost component and still closes cleanly.
	if cal.name != "VCALENDAR" {
		t.Errorf("root component = %q, want VCALENDAR", cal.name)
	}
}

// TestParseICalBoundedTreeSerializes confirms the walkers terminate on the
// bounded tree that over-deep input produces, which is the crash path the bound
// exists to close (a CalDAV REPORT carrying a comp directive reaches
// filterComp/writeComponent on stored bytes).
func TestParseICalBoundedTreeSerializes(t *testing.T) {
	const levels = maxNestingDepth + 500
	src := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		strings.Repeat("BEGIN:VEVENT\r\n", levels) +
		strings.Repeat("END:VEVENT\r\n", levels) +
		"END:VCALENDAR\r\n"

	out, ok := SelectCalendarData([]byte(src), CompSelect{Name: "VCALENDAR", AllProp: true, AllComp: true})
	if !ok {
		t.Fatal("SelectCalendarData returned ok=false")
	}
	if !strings.Contains(string(out), "VERSION:2.0") {
		t.Errorf("projection lost the calendar's own properties\n%s", out)
	}
}

// TestParseICalKeepsLegalNesting confirms the bound leaves real calendars alone:
// VCALENDAR > VEVENT > VALARM is the deepest structure RFC 5545 defines.
func TestParseICalKeepsLegalNesting(t *testing.T) {
	const src = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VEVENT\r\nUID:e1\r\nSUMMARY:Standup\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"

	cal, err := parseICal([]byte(src))
	if err != nil {
		t.Fatalf("parseICal: %v", err)
	}
	ev := cal.sub("VEVENT")
	if ev == nil {
		t.Fatal("VEVENT missing")
	}
	if ev.propText("SUMMARY") != "Standup" {
		t.Errorf("SUMMARY = %q, want Standup", ev.propText("SUMMARY"))
	}
	alarm := ev.sub("VALARM")
	if alarm == nil {
		t.Fatal("VALARM missing")
	}
	if alarm.propText("ACTION") != "DISPLAY" {
		t.Errorf("ACTION = %q, want DISPLAY", alarm.propText("ACTION"))
	}
}

// TestParseICalRefusesMalformedInput covers malformed or truncated input without
// BEGIN:VCALENDAR: parseICal must return errNoCalendar and a nil component tree.
func TestParseICalRefusesMalformedInput(t *testing.T) {
	for _, raw := range []string{
		"",
		"SUMMARY:orphan\r\n",
		"BEGIN:VCAL",
		" \tcontinuation only without any header",
	} {
		cal, err := parseICal([]byte(raw))
		if !errors.Is(err, errNoCalendar) || cal != nil {
			t.Errorf("parseICal(%q) = (%v, %v), want (nil, errNoCalendar)", raw, cal, err)
		}
	}
}

// TestParseICalHandlesUnterminatedFoldedLine covers RFC 5545 section 3.1 line
// unfolding on a folded line that continues to EOF without a closing END or CRLF.
func TestParseICalHandlesUnterminatedFoldedLine(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSUMMARY:part1" +
		strings.Repeat("\r\n part", 100)
	cal, err := parseICal([]byte(raw))
	if err != nil {
		t.Fatalf("parseICal: %v", err)
	}
	ev := cal.sub("VEVENT")
	if ev == nil {
		t.Fatal("VEVENT missing")
	}
	want := "part1" + strings.Repeat("part", 100)
	if got := ev.propText("SUMMARY"); got != want {
		t.Errorf("unfolded SUMMARY length = %d, want %d", len(got), len(want))
	}
}

// TestParseICalTimeRefusesInvalidDates covers RFC 5545 sections 3.3.4 and 3.3.5:
// invalid dates and date-times must be rejected with zero time and ok=false.
func TestParseICalTimeRefusesInvalidDates(t *testing.T) {
	if tm, allDay, ok := parseICalTime(nil); ok || allDay || !tm.IsZero() {
		t.Errorf("parseICalTime(nil) = (%v, %v, %v), want zero false false", tm, allDay, ok)
	}
	for _, val := range []string{"", "not-a-date", "20260230", "20261301T999999Z", "20260612T250000"} {
		l := &iline{name: "DTSTART", params: map[string][]string{}, value: val}
		if tm, allDay, ok := parseICalTime(l); ok || allDay || !tm.IsZero() {
			t.Errorf("parseICalTime(%q) = (%v, %v, %v), want zero false false", val, tm, allDay, ok)
		}
	}
}
