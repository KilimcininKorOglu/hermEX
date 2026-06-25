package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// flaggedAuth is a directory stub whose accounts authenticate and whose GetUser
// reports a configurable must-change-password flag, so the gate can be exercised
// without a database.
type flaggedAuth struct {
	mbox    string
	flagged bool
}

func (a flaggedAuth) Authenticate(string, string) (string, bool) { return a.mbox, true }

func (a flaggedAuth) GetUser(string) (directory.UserDetail, bool, error) {
	return directory.UserDetail{MustChangePassword: a.flagged}, true, nil
}

// TestForcedPasswordChangeGate proves the per-request gate makes the forced change
// real rather than cosmetic: a flagged session is refused on a data endpoint (so it
// cannot bypass the SPA redirect by calling the API directly with its cookie) yet
// keeps the remediation allowlist (/auth/me) reachable, while an unflagged session
// is not gated at all.
func TestForcedPasswordChangeGate(t *testing.T) {
	mbox := t.TempDir()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()
	secret := []byte("forced-change-gate-secret")

	call := func(flagged bool, method, target string) int {
		srv := NewServer(flaggedAuth{mbox: mbox, flagged: flagged}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(method, target, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	// A flagged session is refused on a data endpoint...
	if code := call(true, http.MethodGet, "/api/v1/favorites"); code != http.StatusForbidden {
		t.Errorf("flagged GET /favorites = %d, want 403", code)
	}
	// ...but the remediation allowlist stays reachable so it can change the password.
	if code := call(true, http.MethodGet, "/api/v1/auth/me"); code != http.StatusOK {
		t.Errorf("flagged GET /auth/me = %d, want 200", code)
	}
	// An unflagged session is not gated.
	if code := call(false, http.MethodGet, "/api/v1/favorites"); code != http.StatusOK {
		t.Errorf("unflagged GET /favorites = %d, want 200", code)
	}
}
