package oxcical

import (
	"strings"
	"testing"
	"time"
)

// march9 is the second instance of seriesICal.
var march9 = time.Date(2026, 3, 9, 17, 0, 0, 0, time.UTC)

// instanceCancel builds the organizer's cancellation of the 9 March instance and
// returns the instant an attendee reads from it.
func instanceCancel(t *testing.T) time.Time {
	t.Helper()
	body, ok := InstanceBody([]byte(seriesICal), march9, "CANCEL", 3)
	if !ok {
		t.Fatal("InstanceBody refused a live instance")
	}
	got := string(body)
	for _, want := range []string{"METHOD:CANCEL", "RECURRENCE-ID:20260309T170000Z", "STATUS:CANCELLED", "SEQUENCE:3", "UID:series-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("the cancellation lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "RRULE") {
		t.Errorf("a single-instance cancellation carries the series rule:\n%s", got)
	}
	at, single := OccurrenceInstant(body)
	if !single || !at.Equal(march9) {
		t.Fatalf("OccurrenceInstant = %v, %v; the attendee could not tell which instance", at, single)
	}
	return at
}

// TestInstanceCancelDropsOnlyThatInstance follows an organizer's single-instance
// cancellation to the attendee: the message names exactly the instance, and the
// attendee's copy of the series keeps every other instance.
func TestInstanceCancelDropsOnlyThatInstance(t *testing.T) {
	at := instanceCancel(t)
	marked, ok := CancelInstance([]byte(seriesICal), at)
	if !ok {
		t.Fatal("CancelInstance refused a live instance")
	}
	inst, _ := InstancesIn(marked, march9.Add(-8*24*time.Hour), march9.Add(15*24*time.Hour))
	if len(inst) != 3 {
		t.Errorf("instances after the cancellation = %d, want 3 of the 4 in the window", len(inst))
	}
	for _, i := range inst {
		if i.At.Equal(march9) {
			t.Error("the cancelled instance is still live")
		}
	}
	if !strings.Contains(string(marked), "STATUS:CANCELLED") || !strings.Contains(string(marked), "RRULE:FREQ=WEEKLY") {
		t.Errorf("the copy lost its series or does not record the cancellation:\n%s", marked)
	}
	if _, again := CancelInstance(marked, march9); again {
		t.Error("an instance already cancelled was cancelled again")
	}
}

// TestInstanceRequestMovesTheAttendeeInstance sends a moved instance as a
// single-instance REQUEST and folds it into the attendee's copy.
func TestInstanceRequestMovesTheAttendeeInstance(t *testing.T) {
	moved, ok := MoveOccurrence([]byte(seriesICal), march9, march9.Add(2*time.Hour), march9.Add(3*time.Hour))
	if !ok {
		t.Fatal("MoveOccurrence refused")
	}
	body, ok := InstanceBody(moved, march9, "REQUEST", 1)
	if !ok {
		t.Fatal("InstanceBody refused the moved instance")
	}
	if strings.Contains(string(body), "STATUS:CANCELLED") || !strings.Contains(string(body), "METHOD:REQUEST") {
		t.Fatalf("the update is not a plain request:\n%s", body)
	}
	folded, ok := MergeOverride([]byte(seriesICal), body)
	if !ok {
		t.Fatal("MergeOverride refused the update")
	}
	if !strings.Contains(string(folded), "DTSTART:20260309T190000Z") {
		t.Errorf("the attendee's instance did not move:\n%s", folded)
	}
}

// TestCancelBodyCoversTheWholeMeeting renders a whole-meeting cancellation: one
// VEVENT with no RECURRENCE-ID, so the attendee reads it as the meeting itself even
// when the organizer's copy carries an override.
func TestCancelBodyCoversTheWholeMeeting(t *testing.T) {
	withOverride := mustMerge(t, seriesICal, occurrenceICal)
	body, ok := CancelBody([]byte(withOverride), 5)
	if !ok {
		t.Fatal("CancelBody refused")
	}
	got := string(body)
	for _, want := range []string{"METHOD:CANCEL", "STATUS:CANCELLED", "SEQUENCE:5", "TZID:Europe/Berlin"} {
		if !strings.Contains(got, want) {
			t.Errorf("the cancellation lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "RECURRENCE-ID") || strings.Count(got, "BEGIN:VEVENT") != 1 {
		t.Errorf("a whole-meeting cancellation names an instance:\n%s", got)
	}
	if _, single := OccurrenceInstant(body); single {
		t.Error("the attendee would read the whole-meeting cancellation as one instance")
	}
}

// TestInstanceBodyRefusesAnAbsentInstance keeps a message from naming an instance
// the series never had or has already removed.
func TestInstanceBodyRefusesAnAbsentInstance(t *testing.T) {
	if _, ok := InstanceBody([]byte(seriesICal), march9.Add(time.Hour), "CANCEL", 1); ok {
		t.Error("an instant the rule never generates was accepted")
	}
	removed, _ := CancelOccurrence([]byte(seriesICal), march9)
	if _, ok := InstanceBody(removed, march9, "CANCEL", 1); ok {
		t.Error("an instance already excluded was accepted")
	}
}

// TestSequenceTracksMasterAndOverride reads and writes SEQUENCE on the component
// each message is about.
func TestSequenceTracksMasterAndOverride(t *testing.T) {
	if got := Sequence([]byte(seriesICal), nil); got != 0 {
		t.Errorf("unset sequence = %d, want 0", got)
	}
	bumped, ok := SetSequence([]byte(seriesICal), 2, nil)
	if !ok || Sequence(bumped, nil) != 2 {
		t.Fatalf("master sequence after SetSequence = %d", Sequence(bumped, nil))
	}
	withOverride := mustMerge(t, string(bumped), occurrenceICal)
	if got := Sequence([]byte(withOverride), &march9); got != 0 {
		t.Errorf("override sequence = %d, want its own 0", got)
	}
	ov, ok := SetSequence([]byte(withOverride), 4, &march9)
	if !ok || Sequence(ov, &march9) != 4 || Sequence(ov, nil) != 2 {
		t.Errorf("override = %d, master = %d; want 4 and 2", Sequence(ov, &march9), Sequence(ov, nil))
	}
	if _, ok := SetSequence([]byte(seriesICal), 1, &march9); ok {
		t.Error("SetSequence wrote an override that does not exist")
	}
}

// TestWithMethodReplacesTheMethod keeps exactly one METHOD in a request built from a
// stored object that already carried one.
func TestWithMethodReplacesTheMethod(t *testing.T) {
	src := strings.Replace(seriesICal, "VERSION:2.0\r\n", "VERSION:2.0\r\nMETHOD:PUBLISH\r\n", 1)
	out, ok := WithMethod([]byte(src), "REQUEST")
	if !ok {
		t.Fatal("WithMethod refused")
	}
	if strings.Count(string(out), "METHOD:") != 1 || !strings.Contains(string(out), "METHOD:REQUEST") {
		t.Errorf("METHOD lines wrong:\n%s", out)
	}
	if !strings.Contains(string(out), "RRULE:FREQ=WEEKLY;COUNT=6") {
		t.Errorf("the series was lost:\n%s", out)
	}
}
