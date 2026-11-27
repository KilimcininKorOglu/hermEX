package webmail2api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// policyAuth is a directory that keeps a minimum password length.
type policyAuth struct {
	*guessAuth
	minLen  int
	readErr error
}

func (a *policyAuth) GetMinPasswordLength() (int, bool, error) {
	return a.minLen, true, a.readErr
}

func postPasswordChange(t *testing.T, srv *Server, secret []byte, mbox, newPassword string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: mbox, Jti: "own", Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := mustEncode(t, map[string]string{"currentPassword": "old-password", "newPassword": newPassword})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/account/password", strings.NewReader(string(body)))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestPasswordChangeHonoursTheStoredMinimum proves the self-service change reads
// the operator's minimum instead of a fixed 8: an 11-character password under a
// 12 minimum is refused with a 400 naming 12, nothing is stored, and a 12-character
// one is accepted.
func TestPasswordChangeHonoursTheStoredMinimum(t *testing.T) {
	mbox := t.TempDir()
	secret := []byte("password-policy-secret")
	g := &guessAuth{revokeAuth: newRevokeAuth(mbox, "own")}
	a := &policyAuth{guessAuth: g, minLen: 12}
	srv := revokeServer(t, g.revokeAuth, mbox, secret)
	srv.auth = a

	rec := postPasswordChange(t, srv, secret, mbox, "elevenchars")
	var got struct {
		Error     string `json:"error"`
		MinLength int    `json:"minLength"`
	}
	mustNoErr(t, "decode refusal "+rec.Body.String(), json.Unmarshal(rec.Body.Bytes(), &got))
	if rec.Code != http.StatusBadRequest || got.MinLength != 12 || !strings.Contains(got.Error, "12") {
		t.Fatalf("an 11-character password = %d %s, want 400 naming 12", rec.Code, rec.Body.String())
	}
	if g.changed != "" {
		t.Fatalf("a refused password was stored: %q", g.changed)
	}
	if rec := postPasswordChange(t, srv, secret, mbox, "twelve-chars"); rec.Code != http.StatusOK || g.changed != "twelve-chars" {
		t.Errorf("a 12-character password = %d, stored %q; want 200 and stored", rec.Code, g.changed)
	}
}

// TestPasswordChangeCountsCharacters proves a directory with no stored minimum
// gets the default of 8 counted in characters: "şifre12" is 7 characters in 9
// bytes and must be refused.
func TestPasswordChangeCountsCharacters(t *testing.T) {
	mbox := t.TempDir()
	secret := []byte("password-count-secret")
	g := &guessAuth{revokeAuth: newRevokeAuth(mbox, "own")}
	srv := revokeServer(t, g.revokeAuth, mbox, secret)
	srv.auth = g

	if rec := postPasswordChange(t, srv, secret, mbox, "şifre12"); rec.Code != http.StatusBadRequest {
		t.Errorf("a 7-character, 9-byte password = %d, want 400", rec.Code)
	}
	if rec := postPasswordChange(t, srv, secret, mbox, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty password = %d, want 400", rec.Code)
	}
	if g.changed != "" {
		t.Errorf("a refused password was stored: %q", g.changed)
	}
}

// TestPasswordChangeRefusesWhenTheMinimumIsUnreadable proves an unreadable
// minimum fails the change instead of admitting any length.
func TestPasswordChangeRefusesWhenTheMinimumIsUnreadable(t *testing.T) {
	mbox := t.TempDir()
	secret := []byte("password-unreadable-secret")
	g := &guessAuth{revokeAuth: newRevokeAuth(mbox, "own")}
	a := &policyAuth{guessAuth: g, readErr: errors.New("db down")}
	srv := revokeServer(t, g.revokeAuth, mbox, secret)
	srv.auth = a

	if rec := postPasswordChange(t, srv, secret, mbox, "x"); rec.Code != http.StatusInternalServerError {
		t.Errorf("a change with an unreadable minimum = %d, want 500", rec.Code)
	}
	if g.changed != "" {
		t.Errorf("a password was stored without a readable minimum: %q", g.changed)
	}
}
