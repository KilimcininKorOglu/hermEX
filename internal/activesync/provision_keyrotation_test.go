package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"hermex/internal/easpolicy"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// commandStatusWithKey POSTs a command carrying a specific PolicyKey and returns the HTTP
// status, for asserting the stale-policy-key 449.
func commandStatusWithKey(t *testing.T, ts *httptest.Server, cmd string, root *wbxml.Node, key string) int {
	t.Helper()
	return commandStatusOnDevice(t, ts, "dev1", cmd, root, key)
}

// commandStatusOnDevice is commandStatusWithKey from the named device.
func commandStatusOnDevice(t *testing.T, ts *httptest.Server, device, cmd string, root *wbxml.Node, key string) int {
	t.Helper()
	url := ts.URL + "/Microsoft-Server-ActiveSync?Cmd=" + cmd + "&User=" + testUser + "&DeviceId=" + device + "&DeviceType=iPhone&PolicyKey=" + key
	req, err := http.NewRequest("POST", url, bytes.NewReader(wbxml.Marshal(root)))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser, testPass)
	req.Header.Set("Content-Type", "application/vnd.ms-sync.wbxml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func setOverride(t *testing.T, dir string, p easpolicy.Policy) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetSyncPolicy(p); err != nil {
		t.Fatal(err)
	}
}

func folderSync() *wbxml.Node {
	return wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0"))
}

// provisionDevice runs both Provision phases for dev1 and returns the final key the
// acknowledgement earned.
func provisionDevice(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	temp := provisionPolicy(t, ts, easProvisioningType, "").ChildText(wbxml.PVPolicyKey)
	final := provisionPolicy(t, ts, easProvisioningType, temp)
	wantPolicyStatus(t, "acknowledgement", final, "1")
	return final.ChildText(wbxml.PVPolicyKey)
}

// TestProvisionKeyReflectsPolicy proves the final policy key is the policy's
// generation token: a configured mailbox earns the content-derived key (not the
// baseline) by acknowledging, phase one hands out a temporary key that is none of
// 0, 1 or the final key, and an unconfigured mailbox keeps the baseline "1".
func TestProvisionKeyReflectsPolicy(t *testing.T) {
	ts, dir := seededServer(t)
	override := easpolicy.Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 8}
	setOverride(t, dir, override)
	want := easpolicy.Key(override)

	temp := provisionPolicy(t, ts, easProvisioningType, "").ChildText(wbxml.PVPolicyKey)
	if temp == "0" || temp == "1" || temp == want || temp == "" {
		t.Errorf("phase-one key = %q, want a temporary key other than 0, 1 and the final key", temp)
	}
	if final := provisionPolicy(t, ts, easProvisioningType, temp).ChildText(wbxml.PVPolicyKey); final != want {
		t.Errorf("final key = %q, want the policy generation %q", final, want)
	}

	tsEmpty, _ := seededServer(t)
	if k := provisionPolicy(t, tsEmpty, easProvisioningType, "").ChildText(wbxml.PVPolicyKey); k != "1" {
		t.Errorf("unconfigured key = %q, want the baseline 1", k)
	}
}

// TestStalePolicyKeyForcesReprovision is the load-bearing propagation test: with a policy
// configured, a command with a missing or stale key is refused with 449 while the current
// key passes, and after the policy changes, the previously-valid key goes stale, so an
// already-enrolled device is forced to re-provision and pick up the change.
func TestStalePolicyKeyForcesReprovision(t *testing.T) {
	ts, dir := seededServer(t)
	p1 := easpolicy.Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 4}
	setOverride(t, dir, p1)
	k1 := easpolicy.Key(p1)

	// No key on a configured mailbox → must provision first.
	if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), ""); code != 449 {
		t.Errorf("missing-key FolderSync status %d, want 449", code)
	}
	// A key the device makes up does not pass: the baseline "1" claims a mailbox with
	// no policy, "0" is the not-yet-provisioned key ([MS-ASPROV]), and any other
	// value was never issued.
	for _, forged := range []string{"1", "0", "4294967295", "x"} {
		if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), forged); code != 449 {
			t.Errorf("forged key %q: FolderSync status %d, want 449", forged, code)
		}
	}
	// The current key passes once the device acknowledged it.
	if got := provisionDevice(t, ts); got != k1 {
		t.Fatalf("acknowledgement earned %q, want %q", got, k1)
	}
	if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), k1); code == 449 {
		t.Error("current-key FolderSync was refused with 449")
	}

	// The policy changes; the key the device still holds is now stale until it
	// acknowledges the new policy.
	p2 := easpolicy.Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 8}
	setOverride(t, dir, p2)
	if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), k1); code != 449 {
		t.Errorf("stale-key FolderSync status %d, want 449 (the policy change must propagate)", code)
	}
	if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), easpolicy.Key(p2)); code != 449 {
		t.Errorf("new key before acknowledging the new policy: status %d, want 449", code)
	}
	provisionDevice(t, ts)
	if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), easpolicy.Key(p2)); code == 449 {
		t.Error("the new policy key was refused with 449 after acknowledgement")
	}
}

// TestAPhaseOneKeyIsNotAcknowledgement proves a device cannot skip acknowledging
// the policy. [MS-ASPROV] ("Acknowledging Security Policy Settings", "Responding
// to a Security Policy Settings Acknowledgment"): phase one hands out a temporary
// key, the acknowledgement MUST carry it, and only the reply to that
// acknowledgement carries the final key the device then sends on every command.
//
// The final key is the same for every device under one policy, so carrying it
// proves nothing: a device that never provisioned, or another device of the same
// user, is refused with 449 even when it sends it. A temporary key is consumed by
// its acknowledgement, so acknowledging it a second time is Status 5.
func TestAPhaseOneKeyIsNotAcknowledgement(t *testing.T) {
	ts, dir := seededServer(t)
	policy := easpolicy.Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 4}
	setOverride(t, dir, policy)

	if code := commandStatusOnDevice(t, ts, "dev9", "FolderSync", folderSync(), easpolicy.Key(policy)); code != 449 {
		t.Errorf("never-provisioned device sending the final key: FolderSync status %d, want 449", code)
	}

	temp := provisionPolicy(t, ts, easProvisioningType, "").ChildText(wbxml.PVPolicyKey)
	if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), temp); code != 449 {
		t.Errorf("phase-one key without acknowledgement: FolderSync status %d, want 449", code)
	}

	final := provisionPolicy(t, ts, easProvisioningType, temp)
	wantPolicyStatus(t, "acknowledgement of the phase-one key", final, "1")
	key := final.ChildText(wbxml.PVPolicyKey)
	if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), key); code == 449 {
		t.Error("the key the acknowledgement returned was refused with 449")
	}
	if code := commandStatusOnDevice(t, ts, "dev2", "FolderSync", folderSync(), key); code != 449 {
		t.Errorf("another device sending the key dev1 earned: FolderSync status %d, want 449", code)
	}
	wantPolicyStatus(t, "second acknowledgement of a consumed temporary key",
		provisionPolicy(t, ts, easProvisioningType, temp), "5")
}

// TestNoPolicyNoProvisioningRequired proves an unconfigured mailbox never forces
// provisioning: a command with no policy key proceeds, so unconfigured deployments are
// not churned by this feature.
func TestNoPolicyNoProvisioningRequired(t *testing.T) {
	ts, _ := seededServer(t)
	if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), ""); code == 449 {
		t.Error("an unconfigured mailbox forced provisioning (449); it must not churn devices")
	}
}
