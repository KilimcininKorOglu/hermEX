package webmail

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// sharingServer builds a webmail server whose directory knows alice (the signed-in
// owner, whose mailbox is at path) and bob (a grantable account), and returns it
// with a client already logged in as alice.
func sharingServer(t *testing.T, path string) (*httptest.Server, *http.Client) {
	t.Helper()
	auth := directory.StaticAccounts{
		"alice@hermex.test": {Password: "secret", MailboxPath: path},
		"bob@hermex.test":   {Password: "secret", MailboxPath: t.TempDir()},
	}
	srv, err := NewServer(auth, auth, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	resp, err := cl.PostForm(ts.URL+"/login", url.Values{"user": {"alice@hermex.test"}, "password": {"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return ts, cl
}

// folderPerms reads a folder's stored permission rows as name -> rights.
func folderPerms(t *testing.T, path string, fid int64) map[string]uint32 {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	entries, err := st.ListPermissions(fid)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]uint32{}
	for _, e := range entries {
		m[e.Name] = e.Rights
	}
	return m
}

// memberRowID returns the permission member row id for a named member.
func memberRowID(t *testing.T, path string, fid int64, name string) int64 {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	entries, _ := st.ListPermissions(fid)
	for _, e := range entries {
		if e.Name == name {
			return e.MemberID
		}
	}
	t.Fatalf("no permission row for %s", name)
	return 0
}

// TestFolderSharingLists checks that the sharing page lists who has access to a
// folder and names the matching permission level: a Reviewer grant shows the
// grantee and the "Reviewer" profile, not a raw bitmask.
func TestFolderSharingLists(t *testing.T) {
	path := emptyMailbox(t)
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ModifyPermissions(int64(mapi.PrivateFIDInbox), false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: "bob@hermex.test", Rights: mapi.RightsReviewer},
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	ts := newTestServer(t, path)
	cl := authedClient(t, ts)
	code, body := get(t, cl, ts.URL+"/sharing?folder=INBOX")
	if code != 200 {
		t.Fatalf("sharing status %d, want 200", code)
	}
	if !strings.Contains(body, "bob@hermex.test") {
		t.Errorf("sharing page did not list the grantee:\n%s", body)
	}
	if !strings.Contains(body, "Reviewer") {
		t.Errorf("sharing page did not name the Reviewer level (raw bitmask leaked?):\n%s", body)
	}
}

// TestFolderSharingGrantRevoke checks the write path end to end: granting bob
// Reviewer stores the permission under his canonical login at the Reviewer
// bitmask, and revoking it removes the row.
func TestFolderSharingGrantRevoke(t *testing.T) {
	path := emptyMailbox(t)
	ts, cl := sharingServer(t, path)
	inbox := int64(mapi.PrivateFIDInbox)

	postForm(t, cl, ts.URL+"/sharing", url.Values{
		"op": {"grant"}, "folder": {"INBOX"}, "member": {"bob@hermex.test"}, "level": {"Reviewer"},
	})
	if got := folderPerms(t, path, inbox)["bob@hermex.test"]; got != mapi.RightsReviewer {
		t.Fatalf("after grant bob's rights = %#x, want Reviewer %#x", got, mapi.RightsReviewer)
	}

	postForm(t, cl, ts.URL+"/sharing", url.Values{
		"op": {"revoke"}, "folder": {"INBOX"}, "memberid": {strconv.FormatInt(memberRowID(t, path, inbox, "bob@hermex.test"), 10)},
	})
	if _, ok := folderPerms(t, path, inbox)["bob@hermex.test"]; ok {
		t.Errorf("revoke left bob's permission in place")
	}
}

// TestFolderSharingGrantRejectsUnknown checks that granting to an address no
// mailbox matches is rejected with a message and stores NO permission row, so a
// typo cannot leave an inert grant under an unmatchable name.
func TestFolderSharingGrantRejectsUnknown(t *testing.T) {
	path := emptyMailbox(t)
	ts, cl := sharingServer(t, path)

	_, body := postForm(t, cl, ts.URL+"/sharing", url.Values{
		"op": {"grant"}, "folder": {"INBOX"}, "member": {"nobody@hermex.test"}, "level": {"Reviewer"},
	})
	if !strings.Contains(body, "No mailbox matches") {
		t.Errorf("granting to an unknown address should be rejected with a message:\n%s", body)
	}
	for name, r := range folderPerms(t, path, int64(mapi.PrivateFIDInbox)) {
		if r == mapi.RightsReviewer {
			t.Errorf("an unresolved grant must store no permission row, found one for %q", name)
		}
	}
}
