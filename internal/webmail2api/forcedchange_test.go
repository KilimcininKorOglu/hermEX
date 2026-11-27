package webmail2api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// flaggedAuth is a directory stub whose accounts authenticate and whose GetUser
// reports a configurable must-change-password flag, so the gate can be exercised
// without a database.
type flaggedAuth struct {
	mbox      string
	flagged   bool
	lookupErr error
}

func (a flaggedAuth) Authenticate(string, string) (string, bool) { return a.mbox, true }

func (a flaggedAuth) GetUser(string) (directory.UserDetail, bool, error) {
	if a.lookupErr != nil {
		return directory.UserDetail{}, false, a.lookupErr
	}
	return directory.UserDetail{MustChangePassword: a.flagged}, true, nil
}

// TestAFlaggedSessionCannotWrite: the gate holds for a write too, and the
// refused write changes nothing. A directory that cannot say whether the
// account is flagged refuses rather than letting the session through.
func TestAFlaggedSessionCannotWrite(t *testing.T) {
	mbox := freshMailbox(t)
	secret := []byte("forced-change-gate-secret")
	call := func(auth flaggedAuth, method, target, body string) int {
		srv := NewServer(auth, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
		token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	wantEq(t, "flagged folder create", call(flaggedAuth{mbox: mbox, flagged: true}, http.MethodPost, "/api/v1/folders", `{"name":"Sneaky"}`), http.StatusForbidden)
	wantEq(t, "a session whose flag could not be read",
		call(flaggedAuth{mbox: mbox, lookupErr: errors.New("directory down")}, http.MethodGet, "/api/v1/favorites", ""), http.StatusServiceUnavailable)
	folders, err := openMailbox(t, mbox).ListFolders()
	mustNoErr(t, "list folders", err)
	for _, f := range folders {
		if f.DisplayName == "Sneaky" {
			t.Error("the refused create made the folder")
		}
	}
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
	mustClose(t, st)
	secret := []byte("forced-change-gate-secret")

	call := func(flagged bool, method, target string) int {
		srv := NewServer(flaggedAuth{mbox: mbox, flagged: flagged}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
		token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix()})
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
