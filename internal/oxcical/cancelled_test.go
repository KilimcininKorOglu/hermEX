package oxcical

import (
	"testing"
	"time"
)

// cancelledSeries is a three-day series whose 21 June instance an EXDATE removes.
const cancelledSeries = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:c-1\r\n" +
	"DTSTART:20260620T100000Z\r\nDTEND:20260620T110000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\n" +
	"EXDATE:20260621T100000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// TestCancelledInstancesAreListedApart cancels the 22 June instance. It leaves the
// occupied instances and is the one cancelled instance reported; the instance the
// EXDATE removed is gone rather than cancelled.
func TestCancelledInstancesAreListedApart(t *testing.T) {
	at := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	stored, ok := CancelInstance([]byte(cancelledSeries), at)
	if !ok {
		t.Fatal("CancelInstance refused a live instance")
	}
	from, to := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)

	live, ok := InstancesIn(stored, from, to)
	if !ok || len(live) != 1 || !live[0].At.Equal(time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("occupied instances = %v (ok %v), want only 20 June", live, ok)
	}
	got := CancelledInstancesIn(stored, from, to)
	if len(got) != 1 || !got[0].At.Equal(at) || !got[0].Start.Equal(at) {
		t.Errorf("cancelled instances = %v, want only 22 June", got)
	}
	if none := CancelledInstancesIn([]byte(cancelledSeries), from, to); len(none) != 0 {
		t.Errorf("a series with no cancelled override reported %v", none)
	}
}

// TestCancelInstanceRefusesAnInstanceThatIsNotLive covers the instants a CANCEL
// may name that the series does not hold live: one an EXDATE removed, one the
// rule never generates, one past COUNT, and one already cancelled. Each is
// refused with nothing to store, so a forged or replayed CANCEL cannot recreate
// a removed instance or add one the organizer never scheduled.
func TestCancelInstanceRefusesAnInstanceThatIsNotLive(t *testing.T) {
	cancelledOnce, ok := CancelInstance([]byte(cancelledSeries), time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("CancelInstance refused a live instance")
	}
	for _, c := range []struct {
		name   string
		stored []byte
		at     time.Time
	}{
		{"removed by EXDATE", []byte(cancelledSeries), time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)},
		{"off the rule's time", []byte(cancelledSeries), time.Date(2026, 6, 20, 11, 0, 0, 0, time.UTC)},
		{"past COUNT", []byte(cancelledSeries), time.Date(2026, 6, 23, 10, 0, 0, 0, time.UTC)},
		{"already cancelled", cancelledOnce, time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)},
	} {
		if out, ok := CancelInstance(c.stored, c.at); ok || out != nil {
			t.Errorf("%s: CancelInstance = %d bytes, %v; want refused with nothing to store", c.name, len(out), ok)
		}
	}
}
