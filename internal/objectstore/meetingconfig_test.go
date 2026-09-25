package objectstore

import "testing"

// TestMeetingConfigRoundTrip proves the automatic meeting-processing settings persist
// through the store-root properties, read back as all-false on a fresh mailbox (no
// automatic processing by default), and update independently. The delivery engine
// gates booking on these, so a misread would silently book or ignore invitations.
func TestMeetingConfigRoundTrip(t *testing.T) {
	s := openTestStore(t)

	got, err := s.GetMeetingConfig()
	if err != nil {
		t.Fatalf("GetMeetingConfig on a fresh store: %v", err)
	}
	if got.AutoAccept || got.DeclineRecurring || got.DeclineConflict {
		t.Errorf("fresh store meeting config = %+v, want all false", got)
	}

	want := MeetingConfig{AutoAccept: true, DeclineRecurring: false, DeclineConflict: true}
	if err := s.SetMeetingConfig(want); err != nil {
		t.Fatalf("SetMeetingConfig: %v", err)
	}
	got, err = s.GetMeetingConfig()
	if err != nil {
		t.Fatalf("GetMeetingConfig after set: %v", err)
	}
	if got != want {
		t.Errorf("meeting config = %+v, want %+v", got, want)
	}
}

// TestCancellationProcessingDefaultsOn proves a fresh mailbox processes delivered
// meeting cancellations, that the setting round-trips, and that writing a
// configuration which never names it leaves processing on.
func TestCancellationProcessingDefaultsOn(t *testing.T) {
	s := openTestStore(t)
	got, err := s.GetMeetingConfig()
	if err != nil || got.LeaveCancellationsUnprocessed {
		t.Fatalf("fresh store: leave unprocessed = %v (err %v), want false", got.LeaveCancellationsUnprocessed, err)
	}
	for _, leave := range []bool{true, false} {
		if err := s.SetMeetingConfig(MeetingConfig{AutoAccept: true, LeaveCancellationsUnprocessed: leave}); err != nil {
			t.Fatalf("SetMeetingConfig: %v", err)
		}
		got, err = s.GetMeetingConfig()
		if err != nil || got.LeaveCancellationsUnprocessed != leave || !got.AutoAccept {
			t.Errorf("after set %v: config = %+v (err %v)", leave, got, err)
		}
	}
}
