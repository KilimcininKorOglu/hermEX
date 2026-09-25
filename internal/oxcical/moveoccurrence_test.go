package oxcical

import (
	"strings"
	"testing"
	"time"
)

// TestMoveOccurrenceMovesOnlyThatInstance moves the 30 March instance to 11:00
// UTC: it keeps the series' title, the days around it stay at 09:00 Berlin, and a
// second move of the same instance replaces the first.
func TestMoveOccurrenceMovesOnlyThatInstance(t *testing.T) {
	moved, ok := MoveOccurrence([]byte(berlinDaily), day(30, 7), day(30, 11), day(30, 12))
	if !ok {
		t.Fatal("MoveOccurrence refused a live instance")
	}
	if !strings.Contains(string(moved), "SUMMARY:Standup") || strings.Count(string(moved), "RECURRENCE-ID") != 1 {
		t.Fatalf("override is not one titled instance:\n%s", moved)
	}
	wantStarts(t, moved, day(29, 7), day(30, 11), day(31, 7))

	again, ok := MoveOccurrence(moved, day(30, 7), day(30, 14), day(30, 15))
	if !ok {
		t.Fatal("MoveOccurrence refused an instance it moved before")
	}
	if strings.Count(string(again), "RECURRENCE-ID") != 1 {
		t.Fatalf("a second move added a second override:\n%s", again)
	}
	wantStarts(t, again, day(29, 7), day(30, 14), day(31, 7))
}

// wantStarts holds the instance starts between 29 March and 1 April.
func wantStarts(t *testing.T, ical []byte, want ...time.Time) {
	t.Helper()
	spans := spansIn(t, string(ical), day(29, 0), day(32, 0))
	if len(spans) != len(want) {
		t.Fatalf("spans = %v, want %d instances", spans, len(want))
	}
	for i, s := range spans {
		if !s.Start.Equal(want[i]) {
			t.Errorf("instance %d starts %v", i, s.Start.UTC())
		}
	}
}

// TestHasInstanceNamesOnlyLiveInstances accepts a generated instant and refuses
// one off the pattern and one an EXDATE removed.
func TestHasInstanceNamesOnlyLiveInstances(t *testing.T) {
	if !HasInstance([]byte(berlinDaily), day(30, 7)) {
		t.Error("a generated instant is refused")
	}
	if HasInstance([]byte(berlinDaily), day(30, 8)) {
		t.Error("an instant off the pattern is accepted")
	}
	cancelled, _ := CancelOccurrence([]byte(berlinDaily), day(30, 7))
	if HasInstance(cancelled, day(30, 7)) {
		t.Error("a cancelled instant is accepted")
	}
	if _, ok := MoveOccurrence(cancelled, day(30, 7), day(30, 11), day(30, 12)); ok {
		t.Error("a cancelled instant was moved")
	}
}
