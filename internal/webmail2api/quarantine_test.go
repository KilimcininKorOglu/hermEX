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
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	raw := []byte("From: spammer@x.test\r\nTo: alice@hermex.test\r\nSubject: Spam\r\n\r\nbuy now\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDJunk), raw, time.Now(), 0)
	if err != nil {
		t.Fatalf("append to junk: %v", err)
	}
	st.Close()

	secret := []byte("digest-test-secret")
	accs := directory.StaticAccounts{alice: {Password: "x", MailboxPath: dir}}
	srv := NewServer(accs, accs, nil, "mail.hermex.test", []byte("session-secret"), "", false)
	srv.DigestSecret = secret

	tok, err := quarantine.Mint(secret, quarantine.Claims{Mailbox: alice, UID: info.UID, Expiry: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	// GET shows the confirmation form (not a release yet — prefetch-safe).
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/quarantine/release?t="+url.QueryEscape(tok), nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("confirm page = %d: %s", rec.Code, rec.Body.String())
	}

	// POST performs the release.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/quarantine/release", strings.NewReader("t="+url.QueryEscape(tok)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "moved back to your inbox") {
		t.Fatalf("release = %d: %s", rec.Code, rec.Body.String())
	}

	// The message is now in the Inbox and gone from Junk.
	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	if inbox, _ := st2.ListMessages(int64(mapi.PrivateFIDInbox)); len(inbox) != 1 {
		t.Errorf("inbox has %d messages, want 1", len(inbox))
	}
	if junk, _ := st2.ListMessages(int64(mapi.PrivateFIDJunk)); len(junk) != 0 {
		t.Errorf("junk has %d messages, want 0", len(junk))
	}

	// A garbage token is refused, never reaching a mailbox.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/quarantine/release?t=garbage", nil))
	if !strings.Contains(rec.Body.String(), "invalid or has expired") {
		t.Errorf("bad token should show the expired message: %s", rec.Body.String())
	}
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
