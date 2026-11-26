package dav

import "testing"

// TestEventSpanNeverEndsBeforeItStarts covers a DURATION a sender controls. A
// count too large for a Go duration wrapped to a negative length, so the event's
// span ended before it began and a time-range filter (RFC 4791 section 9.9)
// placed it in the wrong window. An unreadable DURATION is ignored, leaving the
// event its start alone.
func TestEventSpanNeverEndsBeforeItStarts(t *testing.T) {
	for _, dur := range []string{"P200000D", "PT9223372036854775807S", "PD"} {
		ev := &icalNode{name: "VEVENT", props: []icalProp{
			{name: "DTSTART", value: "20260101T090000Z"},
			{name: "DURATION", value: dur},
		}}
		start, end, ok := eventSpan(ev)
		if !ok {
			t.Fatalf("DURATION:%s: eventSpan refused the event", dur)
		}
		if !end.Equal(start) {
			t.Errorf("DURATION:%s: span %v to %v, want the start alone", dur, start, end)
		}
	}
}
