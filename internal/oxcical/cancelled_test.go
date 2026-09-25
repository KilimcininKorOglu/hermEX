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

	live, _ := InstancesIn(stored, from, to)
	if len(live) != 1 || !live[0].At.Equal(time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("occupied instances = %v, want only 20 June", live)
	}
	got := CancelledInstancesIn(stored, from, to)
	if len(got) != 1 || !got[0].At.Equal(at) || !got[0].Start.Equal(at) {
		t.Errorf("cancelled instances = %v, want only 22 June", got)
	}
	if none := CancelledInstancesIn([]byte(cancelledSeries), from, to); len(none) != 0 {
		t.Errorf("a series with no cancelled override reported %v", none)
	}
}
