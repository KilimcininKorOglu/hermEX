package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/quarantine"
)

// TestQuarantineRelease proves a valid digest token confirms then releases the
// named message from Junk back to the Inbox, and a bad token is refused.
func TestQuarantineRelease(t *testing.T) {
	const alice = "alice@hermex.test"
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open store", err)
	raw := []byte("From: spammer@x.test\r\nTo: alice@hermex.test\r\nSubject: Spam\r\n\r\nbuy now\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDJunk), raw, time.Now(), 0)
	mustNoErr(t, "append to junk", err)
	st.Close()

	secret := []byte("digest-test-secret")
	accs := directory.StaticAccounts{alice: {Password: "x", MailboxPath: dir}}
	srv := NewServer(accs, accs, nil, "mail.hermex.test", []byte("session-secret"), "", false)
	srv.DigestSecret = secret
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	tok, err := quarantine.Mint(secret, quarantine.Claims{Mailbox: alice, UID: info.UID, Expiry: time.Now().Add(time.Hour).Unix()})
	mustNoErr(t, "mint token", err)

	// GET shows the confirmation form (not a release yet, prefetch-safe).
	rec := serve(httptest.NewRequest(http.MethodGet, "/quarantine/release?t="+url.QueryEscape(tok), nil))
	wantStatus(t, "confirm page", rec, http.StatusOK)
	wantContains(t, "confirm page", rec.Body.String(), "<form")
	// The page body carries the release token, and this route is outside the API
	// prefix the blanket no-store middleware covers, so the page must say so itself
	// or the token can be recovered from a shared browser's cache and replayed.
	wantEq(t, "confirm page Cache-Control (it embeds the release token)",
		rec.Header().Get("Cache-Control"), "no-store")

	// POST performs the release.
	req := httptest.NewRequest(http.MethodPost, "/quarantine/release", strings.NewReader("t="+url.QueryEscape(tok)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = serve(req)
	wantStatus(t, "release", rec, http.StatusOK)
	wantContains(t, "release page", rec.Body.String(), "moved back to your inbox")

	// The message is now in the Inbox and gone from Junk.
	st2, err := objectstore.Open(dir)
	mustNoErr(t, "reopen mailbox", err)
	defer st2.Close()
	inbox, err := st2.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list inbox", err)
	wantEq(t, "inbox messages after release", len(inbox), 1)
	junk, err := st2.ListMessages(int64(mapi.PrivateFIDJunk))
	mustNoErr(t, "list junk", err)
	wantEq(t, "junk messages after release", len(junk), 0)

	// A garbage token is refused, never reaching a mailbox.
	rec = serve(httptest.NewRequest(http.MethodGet, "/quarantine/release?t=garbage", nil))
	wantContains(t, "bad token page", rec.Body.String(), "invalid or has expired")
}

// TestQuarantineDisabled proves the route 404s when no digest secret is set.
func TestQuarantineDisabled(t *testing.T) {
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", []byte("s"), "", false)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/quarantine/release?t=x", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("disabled release = %d, want 404", rec.Code)
	}
}
