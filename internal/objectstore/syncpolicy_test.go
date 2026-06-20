package objectstore

import (
	"testing"

	"hermex/internal/easpolicy"
)

// TestSyncPolicyRoundTrip proves a mailbox's per-user device-policy override persists,
// reads back nil on a fresh store (so it inherits the global default), and clears.
func TestSyncPolicyRoundTrip(t *testing.T) {
	s := openTestStore(t)

	if got, err := s.GetSyncPolicy(); err != nil || got != nil {
		t.Fatalf("fresh store sync policy = %v, %v; want nil (inherit default)", got, err)
	}

	want := easpolicy.Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 8}
	if err := s.SetSyncPolicy(want); err != nil {
		t.Fatalf("SetSyncPolicy: %v", err)
	}
	got, err := s.GetSyncPolicy()
	if err != nil {
		t.Fatalf("GetSyncPolicy: %v", err)
	}
	if len(got) != 2 || got["DevicePasswordEnabled"] != 1 || got["MinDevicePasswordLength"] != 8 {
		t.Errorf("sync policy = %v, want %v", got, want)
	}

	if err := s.SetSyncPolicy(nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, _ := s.GetSyncPolicy(); got != nil {
		t.Errorf("after clear = %v, want nil", got)
	}
}
