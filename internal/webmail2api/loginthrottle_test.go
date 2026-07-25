package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// rejectAuth is an authenticator that always fails, so the login handler takes the
// failure path on every call and the throttle can accrue.
type rejectAuth struct{}

func (rejectAuth) Authenticate(string, string) (string, bool) { return "", false }

// TestLoginThrottle proves repeated failed logins for one account are locked out
// with 429 once the failure threshold is crossed, blunting online password
// guessing. The default limiter trips at DefaultMaxFails failures.
func TestLoginThrottle(t *testing.T) {
	srv := NewServer(rejectAuth{}, directory.StaticAccounts{}, nil, "mail.hermex.test", []byte("throttle-secret"), "", false)

	post := func() int {
		body := strings.NewReader(`{"email":"victim@hermex.test","password":"guess"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", body)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	// The first failures return 401 (invalid credentials), not yet throttled.
	for i := range 4 {
		if code := post(); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i, code)
		}
	}
	// The fifth failure trips the lockout; the next attempt is refused with 429
	// before credentials are even checked.
	_ = post()
	if code := post(); code != http.StatusTooManyRequests {
		t.Fatalf("after threshold = %d, want 429", code)
	}
}
