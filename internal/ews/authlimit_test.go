package ews

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/authlimit"
	"hermex/internal/directory"
)

// countingAuth counts how often a password is actually checked, which is what the
// throttle exists to stop: each check runs the directory's 600k-round hash.
type countingAuth struct {
	directory.StaticAccounts
	calls *int
}

func (c countingAuth) Authenticate(user, pass string) (string, bool) {
	*c.calls++
	return c.StaticAccounts.Authenticate(user, pass)
}

// throttledServer builds an EWS server with the shared login limiter installed.
func throttledServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	accs := countingAuth{
		StaticAccounts: directory.StaticAccounts{
			testUser:          {Password: testPass, MailboxPath: provisionedDir(t)},
			"bob@hermex.test": {Password: testPass, MailboxPath: provisionedDir(t)},
		},
		calls: &calls,
	}
	srv := NewServer(accs, accs, "mail.hermex.test")
	srv.Limiter = authlimit.New(0, 0, 0)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, &calls
}

// attempt makes one Basic-Auth request and returns its status.
func attempt(t *testing.T, ts *httptest.Server, user, pass string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/EWS/Exchange.asmx", strings.NewReader(wrapRequest(`<GetFolder xmlns="`+nsMessages+`"/>`)))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Content-Type", "text/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	closeBody(t, resp)
	return resp.StatusCode
}

// TestBasicAuthThrottlesGuessing proves repeated failures lock the account out
// and, from then on, the password is not even checked.
func TestBasicAuthThrottlesGuessing(t *testing.T) {
	ts, calls := throttledServer(t)

	for i := range authlimit.DefaultMaxFails {
		if code := attempt(t, ts, testUser, "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", i, code)
		}
	}
	before := *calls
	if code := attempt(t, ts, testUser, "wrong"); code != http.StatusTooManyRequests {
		t.Errorf("status after the cap = %d, want 429", code)
	}
	if *calls != before {
		t.Errorf("the password was checked %d more times while locked out", *calls-before)
	}
	// Even the right password waits out the lockout: the account is the key.
	if code := attempt(t, ts, testUser, testPass); code != http.StatusTooManyRequests {
		t.Errorf("status for the correct password while locked out = %d, want 429", code)
	}
}

// TestBasicAuthThrottleIsPerAccount proves one account's lockout does not take
// the whole deployment down with it.
func TestBasicAuthThrottleIsPerAccount(t *testing.T) {
	ts, _ := throttledServer(t)

	for i := range authlimit.DefaultMaxFails {
		wantEq(t, "unthrottled attempt status", attempt(t, ts, testUser, "wrong"), http.StatusUnauthorized)
		_ = i
	}
	wantEq(t, "locked attempt status", attempt(t, ts, testUser, "wrong"), http.StatusTooManyRequests)
	wantEq(t, "another account wrong pass status", attempt(t, ts, "bob@hermex.test", "wrong"), http.StatusUnauthorized)
	wantEq(t, "another account valid login status", attempt(t, ts, "bob@hermex.test", testPass), http.StatusOK)
	wantEq(t, "locked account still throttled status", attempt(t, ts, testUser, testPass), http.StatusTooManyRequests)
}

// TestBasicAuthSuccessClearsTheCount proves a working client that mistypes a few
// times is not locked out later for those old failures.
func TestBasicAuthSuccessClearsTheCount(t *testing.T) {
	ts, _ := throttledServer(t)

	for range authlimit.DefaultMaxFails - 1 {
		wantEq(t, "pre-clear failure status", attempt(t, ts, testUser, "wrong"), http.StatusUnauthorized)
	}
	wantEq(t, "valid login clears failures", attempt(t, ts, testUser, testPass), http.StatusOK)
	for range authlimit.DefaultMaxFails - 1 {
		wantEq(t, "post-clear failure status", attempt(t, ts, testUser, "wrong"), http.StatusUnauthorized)
	}
	wantEq(t, "post-clear valid login status", attempt(t, ts, testUser, testPass), http.StatusOK)
}

// TestBasicAuthRejectsEmptyAndUnknownCredentials proves missing passwords and
// unknown accounts fail with 401 rather than authenticating.
func TestBasicAuthRejectsEmptyAndUnknownCredentials(t *testing.T) {
	ts, _ := throttledServer(t)
	wantEq(t, "empty password status", attempt(t, ts, testUser, ""), http.StatusUnauthorized)
	wantEq(t, "unknown user status", attempt(t, ts, "nobody@hermex.test", testPass), http.StatusUnauthorized)
}
