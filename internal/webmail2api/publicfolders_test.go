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
