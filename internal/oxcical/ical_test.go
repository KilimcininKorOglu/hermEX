package oxcical

import (
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
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	for range levels {
		b.WriteString("BEGIN:VEVENT\r\n")
	}
	b.WriteString("SUMMARY:deep\r\n")
	for range levels {
		b.WriteString("END:VEVENT\r\n")
	}
	b.WriteString("END:VCALENDAR\r\n")

	cal, err := parseICal([]byte(b.String()))
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
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n")
	for range levels {
		b.WriteString("BEGIN:VEVENT\r\n")
	}
	for range levels {
		b.WriteString("END:VEVENT\r\n")
	}
	b.WriteString("END:VCALENDAR\r\n")

	out, ok := SelectCalendarData([]byte(b.String()), CompSelect{Name: "VCALENDAR", AllProp: true, AllComp: true})
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
