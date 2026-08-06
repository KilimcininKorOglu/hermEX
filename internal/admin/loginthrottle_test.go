package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/authlimit"
	"hermex/internal/directory"
)

// postLogin attempts a JSON admin sign-in with the given password.
func postLogin(t *testing.T, ts *httptest.Server, password string) int {
	t.Helper()
	resp, err := http.Post(ts.URL+"/admin/login", "application/json",
		strings.NewReader(`{"login":"admin@hermex.test","password":"`+password+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// postUILogin attempts an htmx panel sign-in with the given password.
func postUILogin(t *testing.T, ts *httptest.Server, password string) int {
	t.Helper()
	form := url.Values{"login": {"admin@hermex.test"}, "password": {password}}
	resp, err := http.Post(ts.URL+"/admin/ui/login", "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestAdminLoginThrottlesGuessing proves repeated wrong passwords lock the admin
// login out. This is the highest-privilege sign-in in the system, and it was the
// only one of the four (IMAP, POP3, webmail, admin) that accepted unlimited
// guesses.
func TestAdminLoginThrottlesGuessing(t *testing.T) {
	d := &fakeDir{authOK: true, password: "correct", uid: 7,
		roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)

	for i := range authlimit.DefaultMaxFails {
		if code := postLogin(t, ts, "guess"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, code)
		}
	}
	if code := postLogin(t, ts, "guess"); code != http.StatusTooManyRequests {
		t.Errorf("attempt past the limit = %d, want 429", code)
	}
	// The lockout holds even against the right password: an attacker who guesses it
	// on the next try must still wait, which is what makes the throttle worth having.
	if code := postLogin(t, ts, "correct"); code != http.StatusTooManyRequests {
		t.Errorf("correct password during lockout = %d, want 429", code)
	}
}

// TestAdminUILoginSharesTheThrottle proves the htmx panel login is not a way
// around the limit: both entry points reach the same directory, so they must
// share one counter or the throttle is decorative.
func TestAdminUILoginSharesTheThrottle(t *testing.T) {
	d := &fakeDir{authOK: true, password: "correct", uid: 7,
		roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)

	for i := range authlimit.DefaultMaxFails {
		if code := postUILogin(t, ts, "guess"); code != http.StatusUnauthorized {
			t.Fatalf("panel attempt %d = %d, want 401", i+1, code)
		}
	}
	if code := postUILogin(t, ts, "guess"); code != http.StatusTooManyRequests {
		t.Errorf("panel attempt past the limit = %d, want 429", code)
	}
	if code := postLogin(t, ts, "guess"); code != http.StatusTooManyRequests {
		t.Errorf("the JSON login still accepted a guess after the panel exhausted the limit: %d, want 429", code)
	}
}

// TestAdminLoginSuccessClearsTheCount is the control: a few wrong tries followed
// by the right password must not leave the operator locked out afterwards.
func TestAdminLoginSuccessClearsTheCount(t *testing.T) {
	d := &fakeDir{authOK: true, password: "correct", uid: 7,
		roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)

	for range authlimit.DefaultMaxFails - 1 {
		if code := postLogin(t, ts, "guess"); code != http.StatusUnauthorized {
			t.Fatalf("wrong password = %d, want 401", code)
		}
	}
	if code := postLogin(t, ts, "correct"); code != http.StatusOK {
		t.Fatalf("correct password = %d, want 200", code)
	}
	for range authlimit.DefaultMaxFails - 1 {
		if code := postLogin(t, ts, "guess"); code != http.StatusUnauthorized {
			t.Errorf("wrong password after a success = %d, want 401 (the count was not cleared)", code)
		}
	}
}
