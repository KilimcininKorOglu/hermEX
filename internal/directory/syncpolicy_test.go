package directory

import (
	"testing"

	"hermex/internal/easpolicy"
)

// TestDefaultSyncPolicy proves the server-wide default device policy persists, reads
// back nil when unset (no enforcement by default), replaces wholesale on the single
// global row, and clears.
func TestDefaultSyncPolicy(t *testing.T) {
	d, _ := freshDirectory(t)
	readPolicy := func() easpolicy.Policy {
		t.Helper()
		got, err := d.GetDefaultSyncPolicy()
		mustNoErr(t, "get the default sync policy", err)
		return got
	}

	wantEq(t, "policy fields before any is set", len(readPolicy()), 0)

	mustNoErr(t, "set the default policy", d.SetDefaultSyncPolicy(easpolicy.Policy{
		"DevicePasswordEnabled": 1, "MaxInactivityTimeDeviceLock": 900,
	}))
	got := readPolicy()
	wantEq(t, "stored policy fields", len(got), 2)
	wantEq(t, "DevicePasswordEnabled", got["DevicePasswordEnabled"], 1)
	wantEq(t, "MaxInactivityTimeDeviceLock", got["MaxInactivityTimeDeviceLock"], 900)

	// A second set replaces the single global row rather than appending.
	mustNoErr(t, "replace the default policy", d.SetDefaultSyncPolicy(easpolicy.Policy{"AllowCamera": 0}))
	got = readPolicy()
	wantEq(t, "policy fields after the replace", len(got), 1)
	wantEq(t, "AllowCamera", got["AllowCamera"], 0)

	mustNoErr(t, "clear the default policy", d.SetDefaultSyncPolicy(nil))
	wantEq(t, "policy fields after clearing", len(readPolicy()), 0)
}
