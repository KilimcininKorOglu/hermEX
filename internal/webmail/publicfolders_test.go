package webmail

import (
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

// newPublicWebmail builds a webmail server for alice@hermex.test with public
// folders wired, returning the test server and the public-folder service.
func newPublicWebmail(t *testing.T) (*httptest.Server, *publicfolder.Service) {
	t.Helper()
	root := t.TempDir()
	mbox := filepath.Join(root, "alice")
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	auth := directory.StaticAccounts{"alice@hermex.test": {Password: "secret", MailboxPath: mbox}}
	srv, err := NewServer(auth, auth, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	srv.Pub = publicfolder.New(pubPaths{root: root})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv.Pub
}

// TestWebmailPublicFolders covers the read-only public folders browser: the
// discovery link appears, LIST is ACL-filtered, a readable folder's messages and
// a message body render, a folder the caller cannot read is denied, and another
// domain's public folders never appear.
func TestWebmailPublicFolders(t *testing.T) {
	ts, pub := newPublicWebmail(t)

	if err := pub.Provision("hermex.test"); err != nil {
		t.Fatal(err)
	}
	ps, err := pub.OpenForDomain("hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	ann, _ := ps.CreateFolder(nil, "Announcements")
	staff, _ := ps.CreateFolder(nil, "Staff")
	if err := ps.ModifyPermissions(ann, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, MemberID: mapi.MemberIDDefault, Rights: mapi.FrightsVisible | mapi.FrightsReadAny},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ps.ModifyPermissions(staff, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: "bob@hermex.test", Rights: mapi.FrightsVisible | mapi.FrightsReadAny},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AppendMessage(ann, []byte("Subject: PublicHi\r\n\r\npublic body here"), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	ps.Close()

	// Another domain's public folders must never surface to alice.
	if err := pub.Provision("other.test"); err != nil {
		t.Fatal(err)
	}
	os2, err := pub.OpenForDomain("other.test")
	if err != nil {
		t.Fatal(err)
	}
	osecret, _ := os2.CreateFolder(nil, "OtherSecret")
	if err := os2.ModifyPermissions(osecret, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, MemberID: mapi.MemberIDDefault, Rights: mapi.FrightsVisible | mapi.FrightsReadAny},
	}); err != nil {
		t.Fatal(err)
	}
	os2.Close()

	c := authedClient(t, ts)

	// The mail toolbar links to the public folders browser.
	if code, body := get(t, c, ts.URL+"/mail"); code != 200 || !strings.Contains(body, `href="/public-folders"`) {
		t.Fatalf("GET /mail (%d): public folders link present? %v", code, strings.Contains(body, `href="/public-folders"`))
	}

	// Discovery lists the visible folder, hides the one alice cannot see, and never
	// leaks another domain's folder.
	code, body := get(t, c, ts.URL+"/public-folders")
	if code != 200 {
		t.Fatalf("GET /public-folders = %d", code)
	}
	if !strings.Contains(body, "Announcements") {
		t.Errorf("discovery missing Announcements:\n%s", body)
	}
	if strings.Contains(body, "Staff") {
		t.Errorf("discovery leaked Staff (alice has no grant):\n%s", body)
	}
	if strings.Contains(body, "OtherSecret") {
		t.Errorf("discovery leaked another domain's folder:\n%s", body)
	}

	// Opening the readable folder shows its message.
	code, body = get(t, c, ts.URL+"/public-folders?fid="+strconv.FormatInt(ann, 10))
	if code != 200 || !strings.Contains(body, "PublicHi") {
		t.Fatalf("open Announcements (%d): message listed? %v\n%s", code, strings.Contains(body, "PublicHi"), body)
	}

	// Reading the message renders its body.
	code, body = get(t, c, ts.URL+"/public-message?fid="+strconv.FormatInt(ann, 10)+"&uid=1")
	if code != 200 || !strings.Contains(body, "public body here") {
		t.Fatalf("read public message (%d): body present? %v\n%s", code, strings.Contains(body, "public body here"), body)
	}

	// A folder alice cannot read is denied at the message reader.
	if code, _ := get(t, c, ts.URL+"/public-message?fid="+strconv.FormatInt(staff, 10)+"&uid=1"); code != 404 {
		t.Errorf("read denied Staff message = %d, want 404", code)
	}
}

// TestWebmailPublicFoldersDisabled proves the discovery link is absent and the
// page is an empty success when no public-folder service is wired.
func TestWebmailPublicFoldersDisabled(t *testing.T) {
	ts := newTestServer(t, seedMailbox(t)) // no Pub
	c := authedClient(t, ts)
	if code, body := get(t, c, ts.URL+"/mail"); code != 200 || strings.Contains(body, `href="/public-folders"`) {
		t.Errorf("GET /mail (%d): public folders link should be absent without a service", code)
	}
	if code, _ := get(t, c, ts.URL+"/public-folders"); code != 200 {
		t.Errorf("GET /public-folders without a service = %d, want 200 (empty)", code)
	}
}
