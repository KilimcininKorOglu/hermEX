package oxcical

import (
	"strings"
	"testing"
	"time"
)

// seriesICal is a weekly series with one VTIMEZONE and no exception yet.
const seriesICal = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:Europe/Berlin\r\nEND:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:series-1\r\n" +
	"SUMMARY:Weekly\r\n" +
	"DTSTART:20260302T170000Z\r\n" +
	"DTEND:20260302T180000Z\r\n" +
	"RRULE:FREQ=WEEKLY;COUNT=6\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// occurrenceICal moves the 9 March instance an hour later and names a timezone the
// series does not define.
const occurrenceICal = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\nEND:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:series-1\r\n" +
	"SUMMARY:Weekly moved\r\n" +
	"RECURRENCE-ID:20260309T170000Z\r\n" +
	"DTSTART:20260309T180000Z\r\n" +
	"DTEND:20260309T190000Z\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// mustMerge folds the update into the stored object, failing the test when the
// merge refuses.
func mustMerge(t *testing.T, stored, update string) string {
	t.Helper()
	out, ok := MergeOverride([]byte(stored), []byte(update))
	if !ok {
		t.Fatal("MergeOverride refused an occurrence update of a series")
	}
	return string(out)
}

// TestMergeOverrideKeepsTheSeries is the load-bearing case: folding an occurrence
// update must leave the master, its RRULE and its other components in place, and
// add the moved instance as an override.
func TestMergeOverrideKeepsTheSeries(t *testing.T) {
	got := mustMerge(t, seriesICal, occurrenceICal)

	for _, want := range []string{
		"RRULE:FREQ=WEEKLY;COUNT=6",
		"DTSTART:20260302T170000Z",
		"RECURRENCE-ID:20260309T170000Z",
		"DTSTART:20260309T180000Z",
		"TZID:Europe/Berlin",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the merged object lost %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "BEGIN:VEVENT"); n != 2 {
		t.Errorf("VEVENT count = %d, want the master plus one override:\n%s", n, got)
	}
}

// TestMergeOverrideCarriesAMissingTimezone proves an instance whose DTSTART names a
// timezone the series never used is still placeable after the fold.
func TestMergeOverrideCarriesAMissingTimezone(t *testing.T) {
	got := mustMerge(t, seriesICal, occurrenceICal)

	if !strings.Contains(got, "TZID:America/New_York") {
		t.Errorf("the update's own VTIMEZONE was dropped:\n%s", got)
	}
}

// TestMergeOverrideReplacesTheSameInstance proves a second update for one instance
// replaces the first rather than stacking a duplicate override.
func TestMergeOverrideReplacesTheSameInstance(t *testing.T) {
	once := mustMerge(t, seriesICal, occurrenceICal)
	again := strings.Replace(occurrenceICal, "20260309T180000Z", "20260309T190000Z", 1)

	got := mustMerge(t, once, again)

	if n := strings.Count(got, "RECURRENCE-ID:20260309T170000Z"); n != 1 {
		t.Errorf("override count for one instance = %d, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, "DTSTART:20260309T190000Z") {
		t.Errorf("the newer time did not replace the older one:\n%s", got)
	}
}

// TestMergeOverrideRefusesWithoutASeries proves the fold never invents a series: an
// object with no master is left to the caller, which keeps its own behaviour.
func TestMergeOverrideRefusesWithoutASeries(t *testing.T) {
	if _, ok := MergeOverride([]byte(occurrenceICal), []byte(occurrenceICal)); ok {
		t.Error("MergeOverride accepted an object carrying no series master")
	}
	if _, ok := MergeOverride([]byte(seriesICal), []byte(seriesICal)); ok {
		t.Error("MergeOverride accepted a whole series as an occurrence update")
	}
}

// TestOccurrenceInstantReadsTheInstance proves an occurrence update is recognised by
// its RECURRENCE-ID, and a series is not.
func TestOccurrenceInstantReadsTheInstance(t *testing.T) {
	at, ok := OccurrenceInstant([]byte(occurrenceICal))
	if !ok {
		t.Fatal("OccurrenceInstant did not recognise an occurrence update")
	}
	if want := time.Date(2026, 3, 9, 17, 0, 0, 0, time.UTC); !at.Equal(want) {
		t.Errorf("instant = %s, want %s", at, want)
	}
	if _, ok := OccurrenceInstant([]byte(seriesICal)); ok {
		t.Error("a series was read as one occurrence")
	}
}

// TestCancelOccurrenceExcludesTheInstance proves cancelling one instance leaves the
// series standing and excludes just that day.
func TestCancelOccurrenceExcludesTheInstance(t *testing.T) {
	at := time.Date(2026, 3, 9, 17, 0, 0, 0, time.UTC)

	out, ok := CancelOccurrence([]byte(seriesICal), at)
	if !ok {
		t.Fatal("CancelOccurrence refused a series")
	}
	got := string(out)
	if !strings.Contains(got, "RRULE:FREQ=WEEKLY;COUNT=6") {
		t.Errorf("the series was lost:\n%s", got)
	}
	if !strings.Contains(got, "EXDATE:20260309T170000Z") {
		t.Errorf("the cancelled instance was not excluded:\n%s", got)
	}
}

// TestCancelOccurrenceDropsItsOverride proves cancelling an instance that had been
// moved removes the override too, so the moved time does not survive the
// cancellation.
func TestCancelOccurrenceDropsItsOverride(t *testing.T) {
	merged := mustMerge(t, seriesICal, occurrenceICal)
	at := time.Date(2026, 3, 9, 17, 0, 0, 0, time.UTC)

	out, ok := CancelOccurrence([]byte(merged), at)
	if !ok {
		t.Fatal("CancelOccurrence refused a series carrying an override")
	}
	got := string(out)
	if strings.Contains(got, "RECURRENCE-ID:20260309T170000Z") {
		t.Errorf("the cancelled instance kept its override:\n%s", got)
	}
	if !strings.Contains(got, "EXDATE:20260309T170000Z") {
		t.Errorf("the cancelled instance was not excluded:\n%s", got)
	}
}

// TestCancelOccurrenceIsIdempotent proves a repeated cancellation does not stack a
// second EXDATE for the same instant.
func TestCancelOccurrenceIsIdempotent(t *testing.T) {
	at := time.Date(2026, 3, 9, 17, 0, 0, 0, time.UTC)
	once, _ := CancelOccurrence([]byte(seriesICal), at)

	twice, ok := CancelOccurrence(once, at)
	if !ok {
		t.Fatal("the second cancellation was refused")
	}
	if n := strings.Count(string(twice), "EXDATE:20260309T170000Z"); n != 1 {
		t.Errorf("EXDATE count = %d, want 1:\n%s", n, twice)
	}
}
