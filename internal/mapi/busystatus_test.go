package mapi

import "testing"

// TestBusyStatusOccupies pins which statuses take an attendee's time. Every
// scheduler surface asks this one question, and the answer for working elsewhere
// is the whole point: it is the status Outlook writes for a home office day, so
// treating it as busy empties that day of candidate times.
func TestBusyStatusOccupies(t *testing.T) {
	for _, c := range []struct {
		status int32
		name   string
		want   bool
	}{
		{BusyFree, "free", false},
		{BusyTentative, "tentative", true},
		{BusyBusy, "busy", true},
		{BusyOOF, "out of office", true},
		{BusyWorkingElsewhere, "working elsewhere", false},
		// Outside the five: the reference clamps an absent or unknown status to
		// free, so an unrecognised value must not block a meeting.
		{-1, "negative", false},
		{99, "out of range", false},
	} {
		if got := BusyStatusOccupies(c.status); got != c.want {
			t.Errorf("%s (%d) occupies = %v, want %v", c.name, c.status, got, c.want)
		}
	}
}
