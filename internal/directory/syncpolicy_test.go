package directory

import (
	"testing"

	"hermex/internal/easpolicy"
)

// TestDefaultSyncPolicy proves the server-wide default device policy persists, reads
// back nil when unset (no enforcement by default), replaces wholesale on the single
// global row, and clears.
func TestDefaultSyncPolicy(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	if got, err := d.GetDefaultSyncPolicy(); err != nil || got != nil {
		t.Fatalf("fresh default policy = %v, %v; want nil", got, err)
	}

	want := easpolicy.Policy{"DevicePasswordEnabled": 1, "MaxInactivityTimeDeviceLock": 900}
	if err := d.SetDefaultSyncPolicy(want); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetDefaultSyncPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["DevicePasswordEnabled"] != 1 || got["MaxInactivityTimeDeviceLock"] != 900 {
		t.Errorf("default policy = %v, want %v", got, want)
	}

	// A second set replaces the single global row rather than appending.
	if err := d.SetDefaultSyncPolicy(easpolicy.Policy{"AllowCamera": 0}); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetDefaultSyncPolicy()
	if len(got) != 1 || got["AllowCamera"] != 0 {
		t.Errorf("after replace = %v, want a single AllowCamera=0", got)
	}

	if err := d.SetDefaultSyncPolicy(nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetDefaultSyncPolicy(); got != nil {
		t.Errorf("after clear = %v, want nil", got)
	}
}
