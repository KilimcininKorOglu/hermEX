package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/authlimit"
)

// guessAuth accepts only the account's real current password, so a wrong one takes
// the failure path of the change-password check.
type guessAuth struct {
	*revokeAuth
	changed string
}

func (a *guessAuth) Authenticate(_, password string) (string, bool) {
	return a.mbox, password == "old-password"
}

func (a *guessAuth) SetPassword(_, password string) (bool, error) {
	a.changed = password
	return true, nil
}

// TestPasswordChangeThrottlesGuesses proves the current-password check runs under
// the sign-in throttle. A stolen session could otherwise guess the account's
// password here without limit, a path the login throttle never sees. After the
// failure threshold even the right password is refused with 429, as at sign-in,
// and the password is not changed.
func TestPasswordChangeThrottlesGuesses(t *testing.T) {
	mbox := t.TempDir()
	secret := []byte("password-guess-secret")
	a := &guessAuth{revokeAuth: newRevokeAuth(mbox, "stolen")}
	srv := revokeServer(t, a.revokeAuth, mbox, secret)
	srv.auth = a

	post := func(current string) int {
		token, err := mintToken(secret, sessionClaims{
			Email: "alice@hermex.test", Mailbox: mbox, Jti: "stolen", Exp: time.Now().Add(time.Hour).Unix(),
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/account/password",
			strings.NewReader(`{"currentPassword":"`+current+`","newPassword":"new-password"}`))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	for i := range authlimit.DefaultMaxFails {
		if code := post("guess"); code != http.StatusBadRequest {
			t.Fatalf("guess %d = %d, want 400 (wrong current password)", i, code)
		}
	}
	if code := post("old-password"); code != http.StatusTooManyRequests {
		t.Errorf("the right password after %d guesses = %d, want 429", authlimit.DefaultMaxFails, code)
	}
	if a.changed != "" {
		t.Errorf("the password was changed to %q during the lockout", a.changed)
	}
}
