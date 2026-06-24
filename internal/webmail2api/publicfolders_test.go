package webmail2api

import (
	"encoding/json"
	"net/http"
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

// fakePaths maps each domain to its own public-store directory under a test root.
type fakePaths struct{ root string }

func (p fakePaths) HomedirFor(domain string) string {
	return filepath.Join(p.root, "domain", domain)
}

// newTestServer builds a webmail2api Server with a fixed secret for minting test
// session cookies.
func newTestServer(t *testing.T) (*Server, []byte) {
	t.Helper()
	secret := []byte("public-folder-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	return srv, secret
}

// authedGet issues an authenticated GET as email and returns the recorder.
func authedGet(t *testing.T, srv *Server, secret []byte, email, target string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := mintToken(secret, sessionClaims{Email: email, Mailbox: "/unused", Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// seedPublicFolder provisions a domain's public store, creates one folder, grants
// the given user RightsReviewer (visible + read), and appends one message.
func seedPublicFolder(t *testing.T, svc *publicfolder.Service, domain, folder, user string) (fid int64, uid uint32) {
	t.Helper()
	fid, err := svc.CreateFolder(domain, folder)
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	st, err := svc.OpenForDomain(domain)
	if err != nil {
		t.Fatalf("open domain store: %v", err)
	}
	defer st.Close()
	if err := st.ModifyPermissions(fid, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: user, Rights: mapi.RightsReviewer},
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	raw := []byte("From: bob@hermex.test\r\nTo: all@hermex.test\r\nSubject: Welcome\r\n\r\nHello everyone.\r\n")
	info, err := st.AppendMessage(fid, raw, time.Now(), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	return fid, info.UID
}

// authedGetAs issues an authenticated GET as email whose session points at the
// given own-mailbox path, where the caller's per-user public read state lives.
func authedGetAs(t *testing.T, srv *Server, secret []byte, email, mailbox, target string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := mintToken(secret, sessionClaims{Email: email, Mailbox: mailbox, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// folderUnread decodes a public-folders response and returns the Announcements
// folder's per-user unread count.
func folderUnread(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("public-folders status %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Folders []publicFolderJSON `json:"folders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode folders: %v", err)
	}
	for _, f := range list.Folders {
		if f.Name == "Announcements" {
			return f.Unread
		}
	}
	t.Fatalf("Announcements not in %+v", list.Folders)
	return 0
}

// messageRead decodes a folder-messages response and reports the Read flag of the
// message with the given uid.
func messageRead(t *testing.T, rec *httptest.ResponseRecorder, uid uint32) bool {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("messages status %d: %s", rec.Code, rec.Body.String())
	}
	var msgs struct {
		Emails []mailJSON `json:"emails"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	want := strconv.FormatUint(uint64(uid), 10)
	for _, m := range msgs.Emails {
		if m.ID == want {
			return m.Read
		}
	}
	t.Fatalf("uid %d not in %+v", uid, msgs.Emails)
	return false
}

// TestPublicFolderReadStatePersists proves the per-user public-folder read state:
// opening a public message via the webmail2 handler marks it read for THAT user in
// their own store and the read survives a re-list, while a second granted user
// still sees it unread and the shared public store's flag is never touched. This
// is the Exchange-faithful per-user behavior the org-wide shared \Seen flag cannot
// provide.
func TestPublicFolderReadStatePersists(t *testing.T) {
	const domain, alice, bob = "hermex.test", "alice@hermex.test", "bob@hermex.test"
	svc := publicfolder.New(fakePaths{t.TempDir()})
	fid, uid := seedPublicFolder(t, svc, domain, "Announcements", alice)

	// Grant bob the same read access, to prove read state is isolated per user.
	pst, err := svc.OpenForDomain(domain)
	if err != nil {
		t.Fatalf("open domain store: %v", err)
	}
	if err := pst.ModifyPermissions(fid, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: bob, Rights: mapi.RightsReviewer},
	}); err != nil {
		pst.Close()
		t.Fatalf("grant bob: %v", err)
	}
	pst.Close()

	// Each user needs a real own mailbox: their per-user read state lives there.
	aliceBox, bobBox := t.TempDir(), t.TempDir()
	for _, dir := range []string{aliceBox, bobBox} {
		st, err := objectstore.Open(dir)
		if err != nil {
			t.Fatalf("create mailbox %s: %v", dir, err)
		}
		st.Close()
	}

	srv, secret := newTestServer(t)
	srv.Pub = svc

	foldersURL := "/api/v1/public-folders"
	msgsURL := "/api/v1/public-folders/" + strconv.FormatInt(fid, 10) + "/messages"
	openURL := "/api/v1/public-message?fid=" + strconv.FormatInt(fid, 10) + "&uid=" + strconv.FormatUint(uint64(uid), 10)

	// Before reading, alice's badge shows the message unread.
	if u := folderUnread(t, authedGetAs(t, srv, secret, alice, aliceBox, foldersURL)); u != 1 {
		t.Fatalf("alice unread before read = %d, want 1", u)
	}

	// Alice opens the message through the same handler the page uses.
	if rec := authedGetAs(t, srv, secret, alice, aliceBox, openURL); rec.Code != 200 {
		t.Fatalf("open message status %d: %s", rec.Code, rec.Body.String())
	}

	// The read persists across a re-list: alice now sees it read, badge unread = 0.
	if !messageRead(t, authedGetAs(t, srv, secret, alice, aliceBox, msgsURL), uid) {
		t.Errorf("alice message Read = false after open, want true (read did not persist)")
	}
	if u := folderUnread(t, authedGetAs(t, srv, secret, alice, aliceBox, foldersURL)); u != 0 {
		t.Errorf("alice unread after read = %d, want 0", u)
	}

	// Per-user isolation: bob still sees the same message unread.
	if messageRead(t, authedGetAs(t, srv, secret, bob, bobBox, msgsURL), uid) {
		t.Errorf("bob message Read = true, want false (read state must be per-user)")
	}
	if u := folderUnread(t, authedGetAs(t, srv, secret, bob, bobBox, foldersURL)); u != 1 {
		t.Errorf("bob unread = %d, want 1 (per-user)", u)
	}

	// The shared public store's flag was never written: reading stays per-user.
	cst, err := svc.OpenForDomain(domain)
	if err != nil {
		t.Fatalf("reopen domain store: %v", err)
	}
	defer cst.Close()
	info, err := cst.MessageByUID(fid, uid)
	if err != nil {
		t.Fatalf("message by uid: %v", err)
	}
	if info.Flags&objectstore.FlagSeen != 0 {
		t.Errorf("public store flag = \\Seen, want unset (must never write to the public store)")
	}
}

// TestPublicFolders proves a granted caller sees the public folder with its
// counts, lists its messages, and reads one message's body.
func TestPublicFolders(t *testing.T) {
	const domain, alice = "hermex.test", "alice@hermex.test"
	svc := publicfolder.New(fakePaths{t.TempDir()})
	fid, uid := seedPublicFolder(t, svc, domain, "Announcements", alice)

	srv, secret := newTestServer(t)
	srv.Pub = svc

	rec := authedGet(t, srv, secret, alice, "/api/v1/public-folders")
	if rec.Code != 200 {
		t.Fatalf("public-folders status %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Owner   string             `json:"owner"`
		Folders []publicFolderJSON `json:"folders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Owner != domain {
		t.Errorf("owner = %q, want %q", list.Owner, domain)
	}
	var found *publicFolderJSON
	for i := range list.Folders {
		if list.Folders[i].Name == "Announcements" {
			found = &list.Folders[i]
		}
	}
	if found == nil {
		t.Fatalf("Announcements not in %+v", list.Folders)
	}
	if found.Total != 1 {
		t.Errorf("Total = %d, want 1", found.Total)
	}

	rec = authedGet(t, srv, secret, alice, "/api/v1/public-folders/"+strconv.FormatInt(fid, 10)+"/messages")
	var msgs struct {
		Emails []mailJSON `json:"emails"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(msgs.Emails) != 1 || msgs.Emails[0].Subject != "Welcome" {
		t.Fatalf("messages = %+v, want one Welcome", msgs.Emails)
	}

	rec = authedGet(t, srv, secret, alice,
		"/api/v1/public-message?fid="+strconv.FormatInt(fid, 10)+"&uid="+strconv.FormatUint(uint64(uid), 10))
	if rec.Code != 200 {
		t.Fatalf("message status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Hello everyone") {
		t.Errorf("detail missing body: %s", rec.Body.String())
	}
}

// TestPublicFoldersAccessGate proves a same-domain caller WITHOUT a grant, and a
// foreign-domain caller, both see nothing and cannot read a forged fid — the ACL
// gate plus tenant isolation (the domain is derived from the caller).
func TestPublicFoldersAccessGate(t *testing.T) {
	svc := publicfolder.New(fakePaths{t.TempDir()})
	fid, uid := seedPublicFolder(t, svc, "hermex.test", "Announcements", "alice@hermex.test")

	srv, secret := newTestServer(t)
	srv.Pub = svc

	for _, who := range []string{"bob@hermex.test", "eve@evil.test"} {
		rec := authedGet(t, srv, secret, who, "/api/v1/public-folders")
		var list struct {
			Folders []publicFolderJSON `json:"folders"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &list)
		if len(list.Folders) != 0 {
			t.Errorf("%s saw folders: %+v", who, list.Folders)
		}
		rec = authedGet(t, srv, secret, who,
			"/api/v1/public-message?fid="+strconv.FormatInt(fid, 10)+"&uid="+strconv.FormatUint(uint64(uid), 10))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s read a forbidden message: status %d", who, rec.Code)
		}
	}
}
