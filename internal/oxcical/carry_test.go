package oxcical

import (
	"strings"
	"testing"
)

// storedWithExceptions is berlinDaily with the 30 March instance cancelled and
// the 31 March instance moved to 10:00 Berlin.
func storedWithExceptions(t *testing.T) []byte {
	t.Helper()
	cancelled, ok := CancelOccurrence([]byte(berlinDaily), day(30, 7))
	if !ok {
		t.Fatal("CancelOccurrence refused the series")
	}
	moved := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:dst-1\r\nSUMMARY:Standup\r\n" +
		"RECURRENCE-ID:20260331T070000Z\r\nDTSTART:20260331T080000Z\r\nDTEND:20260331T083000Z\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	merged, ok := MergeOverride(cancelled, []byte(moved))
	if !ok {
		t.Fatal("MergeOverride refused the instance")
	}
	return merged
}

// TestCarryExceptionsKeepsThemWhileThePatternHolds renames the series: the
// cancelled day stays cancelled and the moved day stays moved.
func TestCarryExceptionsKeepsThemWhileThePatternHolds(t *testing.T) {
	stored := storedWithExceptions(t)
	renamed := strings.Replace(berlinDaily, "SUMMARY:Standup", "SUMMARY:Daily sync", 1)

	got := CarryExceptions(stored, []byte(renamed))
	if !strings.Contains(string(got), "SUMMARY:Daily sync") {
		t.Fatalf("the edit itself is lost:\n%s", got)
	}
	spans := spansIn(t, string(got), day(30, 0), day(32, 0))
	if len(spans) != 1 || !spans[0].Start.Equal(day(31, 8)) {
		t.Errorf("spans = %v, want only the moved 31 March instance at 08:00 UTC", spans)
	}
}

// TestCarryExceptionsDropsThemWhenThePatternChanges moves the series an hour
// later: the old exceptions name instants the new pattern no longer generates.
func TestCarryExceptionsDropsThemWhenThePatternChanges(t *testing.T) {
	stored := storedWithExceptions(t)
	later := strings.ReplaceAll(berlinDaily, "T090000", "T100000")
	later = strings.ReplaceAll(later, "T093000", "T103000")

	got := CarryExceptions(stored, []byte(later))
	if string(got) != later {
		t.Errorf("a changed pattern kept exceptions:\n%s", got)
	}
	ruled := strings.Replace(berlinDaily, "FREQ=DAILY", "FREQ=WEEKLY", 1)
	if got := CarryExceptions(stored, []byte(ruled)); string(got) != ruled {
		t.Errorf("a changed rule kept exceptions:\n%s", got)
	}
}
