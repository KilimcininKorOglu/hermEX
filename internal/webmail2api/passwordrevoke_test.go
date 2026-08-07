package webmail2api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// revokeAuth is a directory stub that authenticates, stores a password, and keeps
// an in-memory session table, the three capabilities a password change touches.
type revokeAuth struct {
	mbox string

	mu       sync.Mutex
	sessions map[string]string // jti -> email
	deleteFn func(email, keepJti string) (int64, error)
}

func newRevokeAuth(mbox string, jtis ...string) *revokeAuth {
	a := &revokeAuth{mbox: mbox, sessions: map[string]string{}}
	for _, j := range jtis {
		a.sessions[j] = "alice@hermex.test"
	}
	return a
}

func (a *revokeAuth) Authenticate(string, string) (string, bool) { return a.mbox, true }
func (a *revokeAuth) SetPassword(string, string) (bool, error)   { return true, nil }
func (a *revokeAuth) CreateWebmailSession(directory.WebmailSession) error {
	return nil
}
func (a *revokeAuth) TouchWebmailSession(string, int64) error { return nil }

func (a *revokeAuth) WebmailSessionActive(jti string, _ int64) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.sessions[jti]
	return ok, nil
}

func (a *revokeAuth) ListWebmailSessions(string, int64) ([]directory.WebmailSession, error) {
	return nil, nil
}

func (a *revokeAuth) DeleteWebmailSession(_, jti string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.sessions[jti]
	delete(a.sessions, jti)
	return ok, nil
}

func (a *revokeAuth) DeleteOtherWebmailSessions(email, keepJti string) (int64, error) {
	if a.deleteFn != nil {
		return a.deleteFn(email, keepJti)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var n int64
	for jti := range a.sessions {
		if jti != keepJti {
			delete(a.sessions, jti)
			n++
		}
	}
	return n, nil
}

func (a *revokeAuth) alive(jti string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.sessions[jti]
	return ok
}

// changePassword posts a password change as the session named by jti and returns
// the status and decoded body.
func changePassword(t *testing.T, srv *Server, secret []byte, mbox, jti string) (int, map[string]any) {
	t.Helper()
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: mbox, Jti: jti,
		Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/account/password",
		strings.NewReader(`{"currentPassword":"old-password","newPassword":"new-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// revokeServer builds a server over a real (empty) mailbox and the stub directory.
func revokeServer(t *testing.T, a *revokeAuth, mbox string, secret []byte) *Server {
	t.Helper()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	return NewServer(a, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
}

// TestPasswordChangeRevokesOtherSessions is the point of the change. A changed
// password does not invalidate an already-issued token: the per-request check only
// asks whether the session row still exists. So an attacker holding a stolen cookie
// survived the victim changing their password for the rest of the token's lifetime,
// which is exactly the remediation the user believed they had performed.
func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	mbox := t.TempDir()
	secret := []byte("password-revoke-secret")
	a := newRevokeAuth(mbox, "mine", "stolen", "old-phone")
	srv := revokeServer(t, a, mbox, secret)

	code, body := changePassword(t, srv, secret, mbox, "mine")
	if code != http.StatusOK {
		t.Fatalf("change password = %d, want 200 (body %v)", code, body)
	}
	if a.alive("stolen") {
		t.Error("the stolen session still works after the password change")
	}
	if a.alive("old-phone") {
		t.Error("another signed-in device survived the password change")
	}
	if n, _ := body["revokedSessions"].(float64); n != 2 {
		t.Errorf("response reports %v sessions revoked, want 2", body["revokedSessions"])
	}
}

// TestPasswordChangeKeepsTheCallersOwnSession proves the remediation does not log
// the user out of the browser they just performed it from, which is what would push
// them to avoid running it.
func TestPasswordChangeKeepsTheCallersOwnSession(t *testing.T) {
	mbox := t.TempDir()
	secret := []byte("password-revoke-secret")
	a := newRevokeAuth(mbox, "mine", "stolen")
	srv := revokeServer(t, a, mbox, secret)

	if code, body := changePassword(t, srv, secret, mbox, "mine"); code != http.StatusOK {
		t.Fatalf("change password = %d, want 200 (body %v)", code, body)
	}
	if !a.alive("mine") {
		t.Error("the caller's own session was revoked by their own password change")
	}
}

// TestPasswordChangeSucceedsWhenRevocationFails proves the eviction is best-effort.
// The password is already stored by the time this runs, so failing the response
// would tell the user their change did not happen when it did. The reported count
// is what says the eviction did not run.
func TestPasswordChangeSucceedsWhenRevocationFails(t *testing.T) {
	mbox := t.TempDir()
	secret := []byte("password-revoke-secret")
	a := newRevokeAuth(mbox, "mine", "stolen")
	a.deleteFn = func(string, string) (int64, error) { return 0, errors.New("db down") }
	srv := revokeServer(t, a, mbox, secret)

	code, body := changePassword(t, srv, secret, mbox, "mine")
	if code != http.StatusOK {
		t.Fatalf("change password = %d, want 200 despite a revocation failure", code)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Errorf("the password change is not reported as done: %v", body)
	}
	if n, _ := body["revokedSessions"].(float64); n != 0 {
		t.Errorf("a failed revocation reports %v sessions revoked, want 0", body["revokedSessions"])
	}
}
