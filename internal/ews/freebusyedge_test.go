package ews

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// fbBase is a fixed instant the free/busy edge cases are built around.
var fbBase = time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)

// busyEventsIn returns the free/busy events a window reports for one mailbox.
func busyEventsIn(t *testing.T, path string, start, end time.Time) []CalendarEvent {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	events, err := CalendarFreeBusy(st, start, end, false)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// TestFreeBusyExcludesAnAppointmentThatOnlyTouchesTheWindow pins the half-open
// boundary the whole calendar model rests on. An appointment ending exactly when
// the window starts, or starting exactly when it ends, occupies none of the
// window: reporting it makes a scheduler show time as taken that nobody booked,
// and a room refuse a back-to-back meeting.
//
// Both directions are seeded. The appointment ending at the window start catches
// a loosened comparison in CalendarFreeBusy on its own; the one starting at the
// window end is also excluded by the store's window predicate, so it pins the
// behaviour a client sees rather than that one line.
func TestFreeBusyExcludesAnAppointmentThatOnlyTouchesTheWindow(t *testing.T) {
	_, paths := availabilityServer(t)
	path := paths["bob@hermex.test"]

	// Ends exactly when the window begins.
	seedAppointment(t, path, fbBase.Add(-time.Hour), fbBase, mapi.BusyBusy, "before", false)
	// Starts exactly when the window ends.
	seedAppointment(t, path, fbBase.Add(time.Hour), fbBase.Add(2*time.Hour), mapi.BusyBusy, "after", false)

	if got := busyEventsIn(t, path, fbBase, fbBase.Add(time.Hour)); len(got) != 0 {
		t.Errorf("the window reports %d events, want none (both only touch it)", len(got))
	}
}

// TestFreeBusyReportsAnAppointmentThatSharesAnyTime is the other half: the
// boundary must not be loosened into dropping real overlaps.
func TestFreeBusyReportsAnAppointmentThatSharesAnyTime(t *testing.T) {
	for _, c := range []struct {
		name       string
		start, end time.Time
	}{
		{"one minute inside the window", fbBase.Add(-time.Hour), fbBase.Add(time.Minute)},
		{"the whole window", fbBase, fbBase.Add(time.Hour)},
		{"contained within the window", fbBase.Add(15 * time.Minute), fbBase.Add(30 * time.Minute)},
		{"spanning the window", fbBase.Add(-time.Hour), fbBase.Add(2 * time.Hour)},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, paths := availabilityServer(t)
			path := paths["bob@hermex.test"]
			seedAppointment(t, path, c.start, c.end, mapi.BusyBusy, "busy", false)

			if got := busyEventsIn(t, path, fbBase, fbBase.Add(time.Hour)); len(got) != 1 {
				t.Errorf("the window reports %d events, want the overlapping one", len(got))
			}
		})
	}
}
