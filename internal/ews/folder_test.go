package ews

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// seededEWS builds an EWS server over a freshly opened mailbox (its built-in
// folder tree is created by objectstore.Open).
func seededEWS(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st.Close()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

var syncStateRE = regexp.MustCompile(`<(?:\w+:)?SyncState>([^<]*)</(?:\w+:)?SyncState>`)

// extractSyncState pulls the first SyncState element value from a response.
func extractSyncState(xml string) string {
	if m := syncStateRE.FindStringSubmatch(xml); len(m) == 2 {
		return m[1]
	}
	return ""
}

func distinguishedGetFolder(id string) string {
	return wrapRequest(`<GetFolder xmlns="` + nsMessages + `">` +
		`<FolderShape><BaseShape>Default</BaseShape></FolderShape>` +
		`<FolderIds><t:DistinguishedFolderId Id="` + id + `" xmlns:t="` + nsTypes + `"/></FolderIds>` +
		`</GetFolder>`)
}

func findFolderReq(parent, traversal string) string {
	return wrapRequest(`<FindFolder Traversal="` + traversal + `" xmlns="` + nsMessages + `">` +
		`<FolderShape><BaseShape>Default</BaseShape></FolderShape>` +
		`<ParentFolderIds><t:DistinguishedFolderId Id="` + parent + `" xmlns:t="` + nsTypes + `"/></ParentFolderIds>` +
		`</FindFolder>`)
}

func syncHierReq(state string) string {
	body := `<SyncFolderHierarchy xmlns="` + nsMessages + `">` +
		`<FolderShape><BaseShape>Default</BaseShape></FolderShape>`
	if state != "" {
		body += `<SyncState>` + state + `</SyncState>`
	}
	body += `</SyncFolderHierarchy>`
	return wrapRequest(body)
}

// TestGetFolderInbox confirms GetFolder resolves the distinguished inbox to a
// folder element with a display name and counts.
func TestGetFolderInbox(t *testing.T) {
	ts, _ := seededEWS(t)
	resp, out := soapPost(t, ts, distinguishedGetFolder("inbox"), true)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("not a success: %s", out)
	}
	if !strings.Contains(out, "Inbox") {
		t.Errorf("missing Inbox display name: %s", out)
	}
	if !strings.Contains(out, "TotalCount") {
		t.Errorf("missing TotalCount: %s", out)
	}
}

// TestGetFolderUnknownDistinguished confirms an unknown distinguished id yields
// a per-folder error response message, not a fault.
func TestGetFolderUnknownDistinguished(t *testing.T) {
	ts, _ := seededEWS(t)
	resp, out := soapPost(t, ts, distinguishedGetFolder("nosuchfolder"), true)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, `ResponseClass="Error"`) {
		t.Errorf("expected an Error response message: %s", out)
	}
}

// TestFindFolderRoot confirms FindFolder enumerates the top-level folders under
// the message-folder root.
func TestFindFolderRoot(t *testing.T) {
	ts, _ := seededEWS(t)
	resp, out := soapPost(t, ts, findFolderReq("msgfolderroot", "Shallow"), true)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "RootFolder") {
		t.Errorf("missing RootFolder: %s", out)
	}
	if !strings.Contains(out, "Inbox") {
		t.Errorf("Inbox not listed among children: %s", out)
	}
}

// TestSyncFolderHierarchyPrime confirms an empty SyncState reports every folder
// as a Create and returns a fresh SyncState.
func TestSyncFolderHierarchyPrime(t *testing.T) {
	ts, _ := seededEWS(t)
	resp, out := soapPost(t, ts, syncHierReq(""), true)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "Create") {
		t.Errorf("prime reported no Create changes: %s", out)
	}
	if extractSyncState(out) == "" {
		t.Errorf("prime returned no SyncState: %s", out)
	}
}

// TestSyncFolderHierarchyDelta confirms a second sync against the primed
// SyncState reports no changes (the folder set is unchanged).
func TestSyncFolderHierarchyDelta(t *testing.T) {
	ts, _ := seededEWS(t)
	_, out1 := soapPost(t, ts, syncHierReq(""), true)
	token := extractSyncState(out1)
	if token == "" {
		t.Fatalf("no SyncState from prime: %s", out1)
	}
	_, out2 := soapPost(t, ts, syncHierReq(token), true)
	if strings.Contains(out2, "<Create>") || strings.Contains(out2, ":Create>") {
		t.Errorf("delta unexpectedly reported a Create: %s", out2)
	}
}

// TestNextSyncState confirms the opaque token increments and an empty/bad token
// primes to "1".
func TestNextSyncState(t *testing.T) {
	if got := nextSyncState(""); got != "1" {
		t.Errorf("nextSyncState(empty) = %q, want 1", got)
	}
	if got := nextSyncState("4"); got != "5" {
		t.Errorf("nextSyncState(4) = %q, want 5", got)
	}
	if got := nextSyncState("xyz"); got != "1" {
		t.Errorf("nextSyncState(bad) = %q, want 1", got)
	}
}
