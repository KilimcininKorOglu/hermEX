package oxcical

import (
	"strings"
	"testing"
)

const selectFullEvent = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//T//EN\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:UTC\r\nEND:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\nUID:e1\r\nSUMMARY:Standup\r\nDESCRIPTION:Daily\r\nDTSTART:20260701T140000Z\r\n" +
	"LOCATION:Room\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// TestSelectCalendarData keeps only the named property of the VCALENDAR and the named
// properties of the VEVENT, dropping unnamed properties and the unnamed VTIMEZONE (no
// allprop/allcomp).
func TestSelectCalendarData(t *testing.T) {
	sel := CompSelect{
		Name:  "VCALENDAR",
		Props: []PropSelect{{Name: "VERSION"}},
		Comps: []CompSelect{{Name: "VEVENT", Props: []PropSelect{{Name: "UID"}, {Name: "SUMMARY"}}}},
	}
	out, ok := SelectCalendarData([]byte(selectFullEvent), sel)
	if !ok {
		t.Fatal("SelectCalendarData returned ok=false")
	}
	s := string(out)
	if !strings.Contains(s, "VERSION:2.0") || !strings.Contains(s, "UID:e1") || !strings.Contains(s, "SUMMARY:Standup") {
		t.Errorf("a selected property is missing\n%s", s)
	}
	for _, leak := range []string{"PRODID", "VTIMEZONE", "DESCRIPTION", "DTSTART", "LOCATION"} {
		if strings.Contains(s, leak) {
			t.Errorf("unselected %q leaked into the projection\n%s", leak, s)
		}
	}
}

// TestSelectCalendarDataNoValue confirms novalue yields a bare "NAME:" with no value.
func TestSelectCalendarDataNoValue(t *testing.T) {
	sel := CompSelect{
		Name:  "VCALENDAR",
		Comps: []CompSelect{{Name: "VEVENT", Props: []PropSelect{{Name: "SUMMARY", NoValue: true}}}},
	}
	out, _ := SelectCalendarData([]byte(selectFullEvent), sel)
	s := string(out)
	if !strings.Contains(s, "SUMMARY:\r\n") {
		t.Errorf("novalue should yield a bare 'SUMMARY:'\n%s", s)
	}
	if strings.Contains(s, "SUMMARY:Standup") {
		t.Errorf("novalue must drop the value\n%s", s)
	}
}

// TestSelectCalendarDataAllProp confirms allprop keeps every property of a component
// while a missing allcomp still excludes unnamed sub-components.
func TestSelectCalendarDataAllProp(t *testing.T) {
	sel := CompSelect{
		Name:    "VCALENDAR",
		AllProp: true,
		Comps:   []CompSelect{{Name: "VEVENT", AllProp: true}},
	}
	out, _ := SelectCalendarData([]byte(selectFullEvent), sel)
	s := string(out)
	if !strings.Contains(s, "VERSION:2.0") || !strings.Contains(s, "PRODID") || !strings.Contains(s, "DESCRIPTION:Daily") {
		t.Errorf("allprop should keep every property\n%s", s)
	}
	if strings.Contains(s, "VTIMEZONE") {
		t.Errorf("no allcomp should still exclude VTIMEZONE\n%s", s)
	}
}
