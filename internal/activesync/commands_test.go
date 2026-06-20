package activesync

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// seededServer starts an ActiveSync server over a freshly created mailbox (with
// its default folders seeded), authorizing the test user.
func seededServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mbox")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

// galServer starts an ActiveSync server whose directory holds two GAL users that
// share a common prefix ("al"), so a single query resolves to more than one
// match — exercising the multi-recipient paths (RecipientCount, result Range and
// Total) that a single match would not.
func galServer(t *testing.T) *httptest.Server {
	t.Helper()
	accs := directory.StaticAccounts{
		"alice@hermex.test":  {Password: testPass, MailboxPath: filepath.Join(t.TempDir(), "alice")},
		"albert@hermex.test": {Password: testPass, MailboxPath: filepath.Join(t.TempDir(), "albert")},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

// postCommand POSTs a WBXML command to the live endpoint and decodes the reply.
func postCommand(t *testing.T, ts *httptest.Server, cmd string, root *wbxml.Node) (*http.Response, *wbxml.Node) {
	t.Helper()
	url := ts.URL + "/Microsoft-Server-ActiveSync?Cmd=" + cmd + "&User=" + testUser + "&DeviceId=dev1&DeviceType=iPhone"
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
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s status %d: %s", cmd, resp.StatusCode, out)
	}
	node, err := wbxml.Unmarshal(out)
	if err != nil {
		t.Fatalf("decode %s reply: %v\n% x", cmd, err, out)
	}
	return resp, node
}

// TestProvisionTwoPhase confirms the two-phase handshake: phase one returns a
// policy key and a provision document, phase two acknowledges with the key and
// no document.
func TestProvisionTwoPhase(t *testing.T) {
	ts, _ := seededServer(t)

	phase1 := wbxml.Elem(wbxml.PVProvision,
		wbxml.Elem(wbxml.PVPolicies, wbxml.Elem(wbxml.PVPolicy,
			wbxml.Str(wbxml.PVPolicyType, "MS-EAS-Provisioning-WBXML"))))
	_, root := postCommand(t, ts, "Provision", phase1)
	if root.ChildText(wbxml.PVStatus) != "1" {
		t.Errorf("phase 1 status = %q, want 1", root.ChildText(wbxml.PVStatus))
	}
	policy := root.Child(wbxml.PVPolicies).Child(wbxml.PVPolicy)
	key := policy.ChildText(wbxml.PVPolicyKey)
	if key == "" {
		t.Fatal("phase 1 returned no policy key")
	}
	if data := policy.Child(wbxml.PVData); data == nil || data.Child(wbxml.PVEASProvisionDoc) == nil {
		t.Error("phase 1 missing the EAS provision document")
	}

	phase2 := wbxml.Elem(wbxml.PVProvision,
		wbxml.Elem(wbxml.PVPolicies, wbxml.Elem(wbxml.PVPolicy,
			wbxml.Str(wbxml.PVPolicyType, "MS-EAS-Provisioning-WBXML"),
			wbxml.Str(wbxml.PVPolicyKey, key),
			wbxml.Str(wbxml.PVStatus, "1"))))
	_, root2 := postCommand(t, ts, "Provision", phase2)
	if root2.ChildText(wbxml.PVStatus) != "1" {
		t.Errorf("phase 2 status = %q, want 1", root2.ChildText(wbxml.PVStatus))
	}
	if root2.Child(wbxml.PVPolicies).Child(wbxml.PVPolicy).Child(wbxml.PVData) != nil {
		t.Error("phase 2 should not carry a provision document")
	}
}

// rawCommandStatus POSTs a command and returns the HTTP status code without
// requiring success, for asserting the provisioning-required (449) response that
// postCommand would reject.
func rawCommandStatus(t *testing.T, ts *httptest.Server, cmd string, root *wbxml.Node) int {
	t.Helper()
	url := ts.URL + "/Microsoft-Server-ActiveSync?Cmd=" + cmd + "&User=" + testUser + "&DeviceId=dev1&DeviceType=iPhone"
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

// TestProvisionRemoteWipe proves the remote-wipe delivery path end to end: once a
// wipe is queued, any non-Provision command is answered with HTTP 449 to force
// re-provisioning, the Provision response then carries the RemoteWipe directive
// and advances the status to requested, and the device's acknowledgement
// completes the wipe.
func TestProvisionRemoteWipe(t *testing.T) {
	ts, dir := seededServer(t)

	// Record the device first (so a later contact won't reset its wipe status),
	// then queue a full remote wipe.
	postCommand(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	setDeviceWipe(t, st, "dev1", WipeStatusPending)
	st.Close()

	if code := rawCommandStatus(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "1"))); code != 449 {
		t.Fatalf("pending-wipe FolderSync status %d, want 449", code)
	}

	provision := wbxml.Elem(wbxml.PVProvision,
		wbxml.Elem(wbxml.PVPolicies, wbxml.Elem(wbxml.PVPolicy,
			wbxml.Str(wbxml.PVPolicyType, "MS-EAS-Provisioning-WBXML"))))
	_, root := postCommand(t, ts, "Provision", provision)
	if root.Child(wbxml.PVRemoteWipe) == nil {
		t.Error("Provision response missing the RemoteWipe directive")
	}
	st, _ = objectstore.Open(dir)
	if got := deviceWipe(t, st, "dev1"); got != WipeStatusRequested {
		t.Errorf("after delivery status = %d, want requested(%d)", got, WipeStatusRequested)
	}
	st.Close()

	ack := wbxml.Elem(wbxml.PVProvision,
		wbxml.Elem(wbxml.PVRemoteWipe, wbxml.Str(wbxml.PVStatus, "1")))
	postCommand(t, ts, "Provision", ack)
	st, _ = objectstore.Open(dir)
	if got := deviceWipe(t, st, "dev1"); got != WipeStatusWiped {
		t.Errorf("after ack status = %d, want wiped(%d)", got, WipeStatusWiped)
	}
	st.Close()

	// A wiped device is terminal: it is no longer forced to re-provision, so an
	// ordinary command succeeds rather than looping on 449.
	if code := rawCommandStatus(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "1"))); code != http.StatusOK {
		t.Errorf("post-wipe FolderSync status %d, want 200 (no 449 loop)", code)
	}
}

// TestFolderSyncPrime confirms SyncKey 0 returns Status 1, a fresh key, and the
// Inbox (folder type 2) among the changes.
func TestFolderSyncPrime(t *testing.T) {
	ts, dir := seededServer(t)

	_, root := postCommand(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))
	if root.ChildText(wbxml.FHStatus) != "1" {
		t.Errorf("status = %q, want 1", root.ChildText(wbxml.FHStatus))
	}
	if root.ChildText(wbxml.FHSyncKey) != "1" {
		t.Errorf("sync key = %q, want 1", root.ChildText(wbxml.FHSyncKey))
	}
	changes := root.Child(wbxml.FHChanges)
	if changes == nil {
		t.Fatal("no Changes element")
	}
	var inbox bool
	for _, c := range changes.Children {
		if c.Tag == wbxml.FHAdd && c.ChildText(wbxml.FHType) == "2" {
			inbox = true
		}
	}
	if !inbox {
		t.Error("FolderSync prime did not advertise the Inbox (type 2)")
	}

	// The prime must have persisted the device hierarchy state to the store.
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if raw, _ := st.GetActiveSyncState(); raw == "" {
		t.Error("FolderSync prime did not persist the ActiveSync state")
	}
}

// TestFolderSyncIncremental confirms a synced device that re-sends its current
// key gets the same key back with no changes (the v1 hierarchy is static).
func TestFolderSyncIncremental(t *testing.T) {
	ts, _ := seededServer(t)
	postCommand(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))

	_, root := postCommand(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "1")))
	if root.ChildText(wbxml.FHSyncKey) != "1" {
		t.Errorf("sync key = %q, want 1", root.ChildText(wbxml.FHSyncKey))
	}
	if n := root.Child(wbxml.FHChanges).ChildText(wbxml.FHCount); n != "0" {
		t.Errorf("change count = %q, want 0", n)
	}
}

// TestFolderSyncInvalidKey confirms a stale key forces a re-prime via Status 9.
func TestFolderSyncInvalidKey(t *testing.T) {
	ts, _ := seededServer(t)
	postCommand(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))

	_, root := postCommand(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "99")))
	if root.ChildText(wbxml.FHStatus) != "9" {
		t.Errorf("stale-key status = %q, want 9", root.ChildText(wbxml.FHStatus))
	}
}
