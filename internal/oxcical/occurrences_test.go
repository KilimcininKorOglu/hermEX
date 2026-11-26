package oxcical

import (
	"strings"
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

// berlinDaily is a daily 09:00 Berlin standup starting before the 29 March 2026
// switch to summer time.
const berlinDaily = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:dst-1\r\nSUMMARY:Standup\r\n" +
	"DTSTART;TZID=Europe/Berlin:20260327T090000\r\nDTEND;TZID=Europe/Berlin:20260327T093000\r\n" +
	"RRULE:FREQ=DAILY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// TestOccurrencesInKeepsTheZoneWallClock holds a zoned series at its wall time on
// both sides of a daylight saving switch: 09:00 Berlin is 08:00 UTC in winter and
// 07:00 UTC in summer. Expanding on UTC would keep 08:00 UTC, which is 10:00 in
// Berlin after the switch.
func TestOccurrencesInKeepsTheZoneWallClock(t *testing.T) {
	spans := spansIn(t, berlinDaily, day(28, 0), day(31, 0))
	want := []time.Time{day(28, 8), day(29, 7), day(30, 7)}
	if len(spans) != len(want) {
		t.Fatalf("spans = %v, want %v", spans, want)
	}
	for i, s := range spans {
		if !s.Start.Equal(want[i]) {
			t.Errorf("instance %d starts %v, want %v", i, s.Start.UTC(), want[i])
		}
	}
	expanded, ok := ExpandRecurrence([]byte(berlinDaily), day(30, 0), day(31, 0))
	if !ok || !strings.Contains(string(expanded), "DTSTART:20260330T070000Z") {
		t.Errorf("CalDAV expansion misplaces the summer instance:\n%s", expanded)
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

// TestInstanceRunningIntoTheWindowIsFound: an event occupies a window when it
// overlaps it (RFC 4791 section 9.9), so an instance that starts before the window
// and is still running in it is returned. The window holds only the second half
// of the 16 March 17:00-18:00 instance, and only the second hour of a two-hour
// RDATE PERIOD.
func TestInstanceRunningIntoTheWindowIsFound(t *testing.T) {
	spans := spansIn(t, seriesICal, day(16, 17).Add(30*time.Minute), day(16, 18))
	if len(spans) != 1 || !spans[0].Start.Equal(day(16, 17)) || !spans[0].End.Equal(day(16, 18)) {
		t.Errorf("rule instance: spans = %v, want 17:00 to 18:00 on 16 March", spans)
	}
	period := withRDate("RDATE;VALUE=PERIOD:20260305T170000Z/PT2H")
	spans = spansIn(t, period, day(5, 18), day(5, 20))
	if len(spans) != 1 || !spans[0].Start.Equal(day(5, 17)) || !spans[0].End.Equal(day(5, 19)) {
		t.Errorf("period instance: spans = %v, want 17:00 to 19:00 on 5 March", spans)
	}
	out, ok := ExpandRecurrence([]byte(seriesICal), day(16, 17).Add(30*time.Minute), day(16, 18))
	if !ok || !strings.Contains(string(out), "RECURRENCE-ID:20260316T170000Z") {
		t.Errorf("expand did not return the running 16 March instance:\n%s", out)
	}
}

// TestInstanceEndedBeforeTheWindowIsNotFound: reaching back for a running
// instance must not return one that already ended; the 16 March instance ends at
// 18:00, so a window that opens then holds none of it.
func TestInstanceEndedBeforeTheWindowIsNotFound(t *testing.T) {
	if spans := spansIn(t, seriesICal, day(16, 18), day(16, 20)); len(spans) != 0 {
		t.Errorf("spans = %v, want none: the instance ended at the window's start", spans)
	}
	out, ok := ExpandRecurrence([]byte(seriesICal), day(16, 18), day(16, 20))
	if !ok {
		t.Fatal("ExpandRecurrence refused the series")
	}
	if strings.Contains(string(out), "RECURRENCE-ID:20260316T170000Z") {
		t.Errorf("expand returned the ended 16 March instance:\n%s", out)
	}
}
