package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"hermex/internal/directory"
)

// revokingAuth is a directory whose webmail sessions can be revoked mid-test.
type revokingAuth struct {
	directory.StaticAccounts
	revoked atomic.Bool
}

func (a *revokingAuth) CreateWebmailSession(directory.WebmailSession) error { return nil }
func (a *revokingAuth) TouchWebmailSession(string, int64) error             { return nil }
func (a *revokingAuth) WebmailSessionActive(string, int64) (bool, error) {
	return !a.revoked.Load(), nil
}
func (a *revokingAuth) ListWebmailSessions(string, int64) ([]directory.WebmailSession, error) {
	return nil, nil
}
func (a *revokingAuth) DeleteWebmailSession(string, string) (bool, error)        { return false, nil }
func (a *revokingAuth) DeleteOtherWebmailSessions(string, string) (int64, error) { return 0, nil }

// TestEventStreamEndsWhenSessionIsRevoked is the revocation gap. The stream checked
// the session once, at open, then pushed inbox-change signals for its whole
// lifetime. A cookie revoked afterwards (a logout elsewhere, an admin
// compromise-response revoke, a password change) kept reporting the victim's mailbox
// activity until the holder disconnected, which defeats revocation on this channel.
func TestEventStreamEndsWhenSessionIsRevoked(t *testing.T) {
	prev := eventsPollInterval
	eventsPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { eventsPollInterval = prev })

	auth := &revokingAuth{StaticAccounts: directory.StaticAccounts{"alice@hermex.test": {MailboxPath: t.TempDir()}}}
	secret := []byte("events-revoke-test-secret")
	srv := NewServer(auth, auth.StaticAccounts, nil, "mail.hermex.test", secret, "", false)
	// An empty Mailbox keeps every tick free of store I/O. The property under test is
	// the per-tick session re-check, and opening a mailbox 50 times a second makes the
	// stream's exit latency track disk speed instead of that check.
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Jti: "jti-1", Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Handler().ServeHTTP(rec, req)
	}()

	// Let the stream open and tick at least once while the session is still valid,
	// then revoke it: the next tick must end the stream on its own.
	time.Sleep(60 * time.Millisecond)
	auth.revoked.Store(true)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream kept running after the session was revoked")
	}
}
