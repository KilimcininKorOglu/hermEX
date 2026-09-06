package meeting

import (
	"testing"
	"time"

	"hermex/internal/objectstore"
)

// TestAdjacentBookingsAreNotAConflict is the boundary a room stands or falls on.
// A calendar slot is half-open: an appointment ending at 11:30 and one starting
// at 11:30 do not overlap. An inclusive comparison would decline the second, and
// a room that refuses back-to-back meetings is unusable for its whole purpose;
// the only workaround is to stop declining conflicts at all, which permits
// genuine double bookings.
//
// Both directions are checked. The first catches a loosened comparison in
// hasConflict on its own; the second is also excluded by the store's window
// predicate, so it pins the behaviour a client sees rather than that one line.
func TestAdjacentBookingsAreNotAConflict(t *testing.T) {
	for _, c := range []struct {
		name                 string
		existStart, existEnd time.Time
		reqStart, reqEnd     time.Time
	}{
		{
			name:       "the request starts exactly when the booking ends",
			existStart: apBase, existEnd: apBase.Add(30 * time.Minute),
			reqStart: apBase.Add(30 * time.Minute), reqEnd: apBase.Add(time.Hour),
		},
		{
			name:       "the request ends exactly when the booking starts",
			existStart: apBase.Add(30 * time.Minute), existEnd: apBase.Add(time.Hour),
			reqStart: apBase, reqEnd: apBase.Add(30 * time.Minute),
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, tags, accounts := apSetup(t, objectstore.MeetingConfig{AutoAccept: true, DeclineConflict: true})
			seedAppointment(t, st, tags, "", c.existStart, c.existEnd, busyBusy)
			id := seedRequest(t, st, tags, "", c.reqStart, c.reqEnd, false)

			handled, err := AutoProcess(st, accounts, nil, "room@hermex.test", id)
			if err != nil {
				t.Fatal(err)
			}
			if !handled {
				t.Fatal("AutoProcess did not handle the request")
			}
			if got := calBusyStatuses(t, st, tags); len(got) != 2 {
				t.Errorf("calendar holds %d appointments, want both (adjacent bookings do not overlap)", len(got))
			}
		})
	}
}

// TestOverlappingBookingsStillConflict is the other half: the adjacency fix must
// not be a blanket relaxation. A request that shares any time with an existing
// booking is still declined.
func TestOverlappingBookingsStillConflict(t *testing.T) {
	for _, c := range []struct {
		name             string
		reqStart, reqEnd time.Time
	}{
		{"one minute of overlap", apBase.Add(59 * time.Minute), apBase.Add(2 * time.Hour)},
		{"the same slot", apBase, apBase.Add(time.Hour)},
		{"contained within the booking", apBase.Add(15 * time.Minute), apBase.Add(45 * time.Minute)},
		{"containing the booking", apBase.Add(-time.Hour), apBase.Add(2 * time.Hour)},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, tags, accounts := apSetup(t, objectstore.MeetingConfig{AutoAccept: true, DeclineConflict: true})
			seedAppointment(t, st, tags, "", apBase, apBase.Add(time.Hour), busyBusy)
			id := seedRequest(t, st, tags, "", c.reqStart, c.reqEnd, false)

			handled, err := AutoProcess(st, accounts, nil, "room@hermex.test", id)
			if err != nil {
				t.Fatal(err)
			}
			if !handled {
				t.Fatal("AutoProcess did not handle the request")
			}
			if got := calBusyStatuses(t, st, tags); len(got) != 1 {
				t.Errorf("calendar holds %d appointments, want only the existing booking", len(got))
			}
		})
	}
}
