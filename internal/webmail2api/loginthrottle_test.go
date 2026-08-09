package webmail2api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestLoginThrottleCountsTheAddressToo proves the address axis is wired at the
// webmail login: one host working through many accounts is locked out even though
// no single account ever reaches its own threshold. An account-only counter never
// sees a password spray, which is exactly the shape aimed at a whole directory.
func TestLoginThrottleCountsTheAddressToo(t *testing.T) {
	srv := NewServer(rejectAuth{}, directory.StaticAccounts{}, nil, "mail.hermex.test", []byte("throttle-secret"), "", false)
	srv.Limiter().SetLimits(2, time.Minute, time.Minute) // address axis trips at 2*4

	post := func(addr, email string) int {
		body := strings.NewReader(`{"email":"` + email + `","password":"guess"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", body)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	const attacker = "198.51.100.7:41000"
	for i := range 8 {
		email := fmt.Sprintf("user%d@hermex.test", i) // a different account every time
		if code := post(attacker, email); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401 (no account is near its own threshold)", i, code)
		}
	}
	if code := post(attacker, "fresh@hermex.test"); code != http.StatusTooManyRequests {
		t.Errorf("the spraying host = %d, want 429", code)
	}
	// Negative control: another host is untouched, so the axis refuses the guesser
	// rather than the service.
	if code := post("203.0.113.9:41000", "fresh@hermex.test"); code != http.StatusUnauthorized {
		t.Errorf("an unrelated host = %d, want 401", code)
	}
}
