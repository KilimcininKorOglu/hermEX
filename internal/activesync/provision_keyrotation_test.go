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
	url := ts.URL + "/Microsoft-Server-ActiveSync?Cmd=" + cmd + "&User=" + testUser + "&DeviceId=dev1&DeviceType=iPhone&PolicyKey=" + key
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

// TestProvisionKeyReflectsPolicy proves the issued policy key is the policy's generation
// token: a configured mailbox gets the content-derived key (not the baseline), and an
// unconfigured one keeps the baseline "1".
func TestProvisionKeyReflectsPolicy(t *testing.T) {
	ts, dir := seededServer(t)
	override := easpolicy.Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 8}
	setOverride(t, dir, override)

	phase1 := wbxml.Elem(wbxml.PVProvision,
		wbxml.Elem(wbxml.PVPolicies, wbxml.Elem(wbxml.PVPolicy,
			wbxml.Str(wbxml.PVPolicyType, "MS-EAS-Provisioning-WBXML"))))
	_, root := postCommand(t, ts, "Provision", phase1)
	key := root.Child(wbxml.PVPolicies).Child(wbxml.PVPolicy).ChildText(wbxml.PVPolicyKey)
	if want := easpolicy.Key(override); key != want {
		t.Errorf("issued key = %q, want the policy generation %q", key, want)
	}
	if key == "1" {
		t.Error("a configured policy issued the baseline key 1; changes could not be detected")
	}

	tsEmpty, _ := seededServer(t)
	_, rootE := postCommand(t, tsEmpty, "Provision", phase1)
	if k := rootE.Child(wbxml.PVPolicies).Child(wbxml.PVPolicy).ChildText(wbxml.PVPolicyKey); k != "1" {
		t.Errorf("unconfigured key = %q, want the baseline 1", k)
	}
}

// TestStalePolicyKeyForcesReprovision is the load-bearing propagation test: with a policy
// configured, a command with a missing or stale key is refused with 449 while the current
// key passes — and after the policy changes, the previously-valid key goes stale, so an
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
	// The current key passes.
	if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), k1); code == 449 {
		t.Error("current-key FolderSync was refused with 449")
	}

	// The policy changes; the key the device still holds is now stale.
	p2 := easpolicy.Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 8}
	setOverride(t, dir, p2)
	if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), k1); code != 449 {
		t.Errorf("stale-key FolderSync status %d, want 449 (the policy change must propagate)", code)
	}
	if code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), easpolicy.Key(p2)); code == 449 {
		t.Error("the new policy key was refused with 449")
	}
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
