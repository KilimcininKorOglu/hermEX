package oxcical

import (
	"testing"
	"time"
)

// day returns a UTC instant on the 2026 test dates.
func day(d, h int) time.Time { return time.Date(2026, 3, d, h, 0, 0, 0, time.UTC) }

// spansIn expands the object over the window, failing the test when it is not a
// series.
func spansIn(t *testing.T, ical string, from, to time.Time) []Span {
	t.Helper()
	spans, ok := OccurrencesIn([]byte(ical), from, to)
	if !ok {
		t.Fatal("OccurrencesIn refused a series")
	}
	return spans
}

// TestOccurrencesInFindsTheInstanceInTheWindow is what a conflict check needs: a
// series occupies the days its instances fall on, not only its first one.
func TestOccurrencesInFindsTheInstanceInTheWindow(t *testing.T) {
	// The series starts 2 March; the window is the third instance, 16 March.
	spans := spansIn(t, seriesICal, day(16, 16), day(16, 20))

	if len(spans) != 1 {
		t.Fatalf("spans = %v, want the 16 March instance", spans)
	}
	if !spans[0].Start.Equal(day(16, 17)) || !spans[0].End.Equal(day(16, 18)) {
		t.Errorf("span = %v, want 17:00 to 18:00", spans[0])
	}
}

// TestOccurrencesInSkipsAnExcludedInstance proves a cancelled day frees the slot.
func TestOccurrencesInSkipsAnExcludedInstance(t *testing.T) {
	cancelled, ok := CancelOccurrence([]byte(seriesICal), day(9, 17))
	if !ok {
		t.Fatal("CancelOccurrence refused the series")
	}

	if spans := spansIn(t, string(cancelled), day(9, 16), day(9, 20)); len(spans) != 0 {
		t.Errorf("spans = %v, want none on an excluded day", spans)
	}
}

// TestOccurrencesInFollowsAMovedInstance is the load-bearing override case: a moved
// instance occupies its new time and leaves its old one free.
func TestOccurrencesInFollowsAMovedInstance(t *testing.T) {
	merged := mustMerge(t, seriesICal, occurrenceICal) // 9 March moves 17:00 -> 18:00

	if spans := spansIn(t, merged, day(9, 17), day(9, 18)); len(spans) != 0 {
		t.Errorf("spans = %v, want the original slot free", spans)
	}
	spans := spansIn(t, merged, day(9, 18), day(9, 19))
	if len(spans) != 1 || !spans[0].Start.Equal(day(9, 18)) {
		t.Errorf("spans = %v, want the moved instance at 18:00", spans)
	}
}

// TestOccurrencesInDropsACancelledOverride proves an instance whose override says
// CANCELLED occupies nothing.
func TestOccurrencesInDropsACancelledOverride(t *testing.T) {
	cancelledOverride := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VEVENT\r\nUID:series-1\r\nRECURRENCE-ID:20260309T170000Z\r\n" +
		"DTSTART:20260309T170000Z\r\nDTEND:20260309T180000Z\r\nSTATUS:CANCELLED\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	merged := mustMerge(t, seriesICal, cancelledOverride)

	if spans := spansIn(t, merged, day(9, 16), day(9, 20)); len(spans) != 0 {
		t.Errorf("spans = %v, want none for a cancelled instance", spans)
	}
}

// TestOccurrencesInRefusesANonSeries proves a plain event is left to the caller's
// own start and end.
func TestOccurrencesInRefusesANonSeries(t *testing.T) {
	if _, ok := OccurrencesIn([]byte(occurrenceICal), day(9, 0), day(10, 0)); ok {
		t.Error("OccurrencesIn expanded an object carrying no series master")
	}
}
