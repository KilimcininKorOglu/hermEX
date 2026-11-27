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
	inst, ok := InstancesIn(marked, march9.Add(-8*24*time.Hour), march9.Add(15*24*time.Hour))
	if !ok || len(inst) != 3 {
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
	if out, again := CancelInstance(marked, march9); again || out != nil {
		t.Errorf("an instance already cancelled was cancelled again (%d bytes to store)", len(out))
	}
}

// TestInstanceCancelledReadsTheAttendeeCopy reads back the state CancelInstance
// leaves: the called-off instance, and no other, reads as cancelled until it is
// removed, and an object that is not a series says so.
func TestInstanceCancelledReadsTheAttendeeCopy(t *testing.T) {
	marked, ok := CancelInstance([]byte(seriesICal), march9)
	if !ok {
		t.Fatal("CancelInstance refused a live instance")
	}
	wantInstanceCancelled(t, marked, march9, true, true, "the cancelled instance")
	wantInstanceCancelled(t, marked, march9.Add(7*24*time.Hour), false, true, "an instance that is still on")
	removed, ok := CancelOccurrence(marked, march9)
	if !ok {
		t.Fatal("CancelOccurrence refused the cancelled instance")
	}
	wantInstanceCancelled(t, removed, march9, false, true, "a removed instance")
	single := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:one\r\n" +
		"DTSTART:20260309T170000Z\r\nDTEND:20260309T180000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	wantInstanceCancelled(t, []byte(single), march9, false, false, "a single event")
}

// wantInstanceCancelled fails the test unless InstanceCancelled answers the
// expected pair for the instance at at.
func wantInstanceCancelled(t *testing.T, stored []byte, at time.Time, wantCancelled, wantSeries bool, what string) {
	t.Helper()
	if cancelled, series := InstanceCancelled(stored, at); cancelled != wantCancelled || series != wantSeries {
		t.Errorf("%s reads cancelled=%v series=%v, want %v %v", what, cancelled, series, wantCancelled, wantSeries)
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
	if at, single := OccurrenceInstant(body); single || !at.IsZero() {
		t.Errorf("the attendee would read the whole-meeting cancellation as the instance at %s", at)
	}
}

// TestInstanceBodyRefusesAnAbsentInstance keeps a message from naming an instance
// the series never had or has already removed.
func TestInstanceBodyRefusesAnAbsentInstance(t *testing.T) {
	if out, ok := InstanceBody([]byte(seriesICal), march9.Add(time.Hour), "CANCEL", 1); ok || out != nil {
		t.Errorf("an instant the rule never generates was accepted (%d bytes)", len(out))
	}
	removed, ok := CancelOccurrence([]byte(seriesICal), march9)
	if !ok {
		t.Fatal("CancelOccurrence refused a live instance")
	}
	if out, ok := InstanceBody(removed, march9, "CANCEL", 1); ok || out != nil {
		t.Errorf("an instance already excluded was accepted (%d bytes)", len(out))
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
	if out, ok := SetSequence([]byte(seriesICal), 1, &march9); ok || out != nil {
		t.Errorf("SetSequence wrote an override that does not exist (%d bytes)", len(out))
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

// wantRefused fails unless an iTIP builder returned nil and false.
func wantRefused(t *testing.T, name string, out []byte, ok bool) {
	t.Helper()
	if ok || out != nil {
		t.Errorf("%s on malformed input = (%d bytes, %v), want (nil, false)", name, len(out), ok)
	}
}

// TestITIPRefusesMalformedInput ensures malformed or truncated iCalendar bytes
// produce no partial iTIP body and safe zero defaults.
func TestITIPRefusesMalformedInput(t *testing.T) {
	bad := []byte("NOT-ICALENDAR\r\n")
	out, ok := WithMethod(bad, "REQUEST")
	wantRefused(t, "WithMethod", out, ok)
	out, ok = CancelBody(bad, 1)
	wantRefused(t, "CancelBody", out, ok)
	out, ok = InstanceBody(bad, march9, "CANCEL", 1)
	wantRefused(t, "InstanceBody", out, ok)
	out, ok = CancelInstance(bad, march9)
	wantRefused(t, "CancelInstance", out, ok)
	out, ok = SetSequence(bad, 1, nil)
	wantRefused(t, "SetSequence", out, ok)
	wantEq(t, Method(bad), "", "Method on malformed input")
	wantEq(t, Sequence(bad, nil), 0, "Sequence on malformed input")
}
