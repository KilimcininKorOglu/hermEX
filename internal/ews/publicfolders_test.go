package ews

import (
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/publicfolder"
)

// pubPaths maps a domain to its own public-store directory under a test root.
type pubPaths struct{ root string }

func (p pubPaths) HomedirFor(domain string) string {
	return filepath.Join(p.root, "public", domain)
}

// publicEWS builds an EWS server for alice@hermex.test with a public-folder service
// rooted under a temp dir, plus a real private mailbox for the caller.
func publicEWS(t *testing.T) (*httptest.Server, *publicfolder.Service) {
	t.Helper()
	root := t.TempDir()
	userDir := filepath.Join(root, "user")
	st, err := objectstore.Open(userDir)
	if err != nil {
		t.Fatalf("open caller mailbox: %v", err)
	}
	st.Close()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: userDir}}
	pub := publicfolder.New(pubPaths{root: root})
	srv := NewServer(accs, accs, "mail.hermex.test")
	srv.Pub = pub
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, pub
}

var ewsFolderIDRE = regexp.MustCompile(`<(?:t:)?FolderId Id="([^"]+)"`)

func getFolderByID(id string) string {
	return wrapRequest(`<GetFolder xmlns="` + nsMessages + `">` +
		`<FolderShape><BaseShape>Default</BaseShape></FolderShape>` +
		`<FolderIds><t:FolderId Id="` + id + `" xmlns:t="` + nsTypes + `"/></FolderIds>` +
		`</GetFolder>`)
}

// TestPublicFolderRootACLFiltered proves FindFolder on publicfoldersroot returns
// the public folders the caller may see (the anyone-granted Announcements) and not
// those they may not (Staff, granted only to another user), and that a returned id
// round-trips through GetFolder back to the same public store.
func TestPublicFolderRootACLFiltered(t *testing.T) {
	ts, pub := publicEWS(t)
	if err := pub.Provision("hermex.test"); err != nil {
		t.Fatal(err)
	}
	st, err := pub.OpenForDomain("hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	ann, err := st.CreateFolder(nil, "Announcements")
	if err != nil {
		t.Fatal(err)
	}
	staff, err := st.CreateFolder(nil, "Staff")
	if err != nil {
		t.Fatal(err)
	}
	// Announcements: anyone in the domain may see+read. Staff: only bob (not alice).
	if err := st.ModifyPermissions(ann, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, MemberID: mapi.MemberIDDefault, Rights: mapi.FrightsVisible | mapi.FrightsReadAny},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ModifyPermissions(staff, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: "bob@hermex.test", Rights: mapi.FrightsVisible | mapi.FrightsReadAny},
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	resp, out := soapPost(t, ts, findFolderReq("publicfoldersroot", "Shallow"), true)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("not a success: %s", out)
	}
	if !strings.Contains(out, "Announcements") {
		t.Errorf("alice should see the anyone-granted Announcements:\n%s", out)
	}
	if strings.Contains(out, "Staff") {
		t.Errorf("alice must not see Staff (granted only to bob):\n%s", out)
	}

	// The returned folder id round-trips: GetFolder on it reaches the same public
	// store (not the caller's mailbox) and resolves the folder.
	m := ewsFolderIDRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no folder id in response:\n%s", out)
	}
	resp2, out2 := soapPost(t, ts, getFolderByID(m[1]), true)
	if resp2.StatusCode != 200 || !strings.Contains(out2, `ResponseClass="Success"`) {
		t.Fatalf("round-trip GetFolder failed (%d): %s", resp2.StatusCode, out2)
	}
	if !strings.Contains(out2, "Announcements") {
		t.Errorf("round-trip GetFolder did not resolve the public folder:\n%s", out2)
	}
}

// TestPublicFolderRootEmptyWhenUnprovisioned proves a domain with no public store
// returns a successful, empty public folders root rather than an error — the same
// observable result as a provisioned store the caller can see nothing in.
func TestPublicFolderRootEmptyWhenUnprovisioned(t *testing.T) {
	ts, _ := publicEWS(t) // never provisioned
	resp, out := soapPost(t, ts, findFolderReq("publicfoldersroot", "Shallow"), true)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("un-provisioned publicfoldersroot should be an empty success:\n%s", out)
	}
}

// TestPublicFolderRootDisabledWithoutService proves that when the server has no
// public-folder service wired, publicfoldersroot is an empty success (feature off),
// never a crash.
func TestPublicFolderRootDisabledWithoutService(t *testing.T) {
	ts, _ := seededEWS(t) // NewServer without Pub set
	resp, out := soapPost(t, ts, findFolderReq("publicfoldersroot", "Shallow"), true)
	if resp.StatusCode != 200 || !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("publicfoldersroot without a service should be an empty success (%d):\n%s", resp.StatusCode, out)
	}
}
