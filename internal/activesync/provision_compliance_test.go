package activesync

import (
	"testing"

	"hermex/internal/easpolicy"
	"hermex/internal/wbxml"
)

// TestProvisionGivesTheKeyOnlyToAFullyCompliantDevice proves a device earns the
// final policy key only by reporting the policy applied in full (Status 1 in its
// acknowledgement). [MS-ASPROV] Provision command errors: a device that applied the
// policy only in part is refused with 139, and an externally managed one (4) with
// 145. A device that applied none of it (3), or reports nothing, cannot comply
// either and is refused with 139. A refused device gets no key, its commands keep
// answering 449, and it can still comply afterwards with a fresh exchange.
func TestProvisionGivesTheKeyOnlyToAFullyCompliantDevice(t *testing.T) {
	ts, dir := seededServer(t)
	policy := easpolicy.Policy{"DevicePasswordEnabled": 1}
	setOverride(t, dir, policy)

	for _, tc := range []struct{ applied, want string }{
		{"2", "139"},
		{"3", "139"},
		{"4", "145"},
		{"", "139"},
	} {
		temp := provisionPolicy(t, ts, easProvisioningType, "").ChildText(wbxml.PVPolicyKey)
		root := provisionAck(t, ts, easProvisioningType, temp, tc.applied)
		if got := root.ChildText(wbxml.PVStatus); got != tc.want {
			t.Errorf("acknowledgement reporting %q: Provision Status = %q, want %s", tc.applied, got, tc.want)
		}
		if p := root.Child(wbxml.PVPolicies); p != nil && p.Child(wbxml.PVPolicy).ChildText(wbxml.PVPolicyKey) != "" {
			t.Errorf("acknowledgement reporting %q was handed a policy key", tc.applied)
		}
		if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), easpolicy.Key(policy)); code != 449 {
			t.Errorf("after an acknowledgement reporting %q: FolderSync status %d, want 449", tc.applied, code)
		}
	}

	if got := provisionDevice(t, ts); got != easpolicy.Key(policy) {
		t.Errorf("a compliant device after refusals earned %q, want the final key", got)
	}
}
