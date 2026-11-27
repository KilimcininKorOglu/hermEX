package dav

import (
	"net/http"
	"net/http/httptest"
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

// throttledServer builds a server with the shared login limiter installed.
func throttledServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	accs := countingAuth{
		StaticAccounts: directory.StaticAccounts{
			testUser:          {Password: testPass, MailboxPath: t.TempDir()},
			"bob@hermex.test": {Password: testPass, MailboxPath: t.TempDir()},
		},
		calls: &calls,
	}
	srv := NewServer(accs, accs, "hermex.test")
	srv.Limiter = authlimit.New(0, 0, 0)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, &calls
}

// attempt makes one Basic-Auth request and returns its status.
func attempt(t *testing.T, ts *httptest.Server, user, pass string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/.well-known/carddav", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, resp.Body)
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
	ts, calls := throttledServer(t)

	failUntilLocked(t, ts, testUser, authlimit.DefaultMaxFails)
	if code := attempt(t, ts, testUser, "wrong"); code != http.StatusTooManyRequests {
		t.Fatalf("the guessed account status = %d, want 429 once locked out", code)
	}
	before := *calls
	if code := attempt(t, ts, "bob@hermex.test", testPass); code == http.StatusTooManyRequests || code == http.StatusUnauthorized {
		t.Errorf("another account's correct login = %d, want it served", code)
	}
	if *calls != before+1 {
		t.Errorf("another account's password was checked %d times, want 1", *calls-before)
	}
}

// failUntilLocked sends n wrong passwords for user and requires each to be
// refused with 401 before any lockout applies.
func failUntilLocked(t *testing.T, ts *httptest.Server, user string, n int) {
	t.Helper()
	for i := range n {
		if code := attempt(t, ts, user, "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("wrong password %d status = %d, want 401", i+1, code)
		}
	}
}

// TestBasicAuthSuccessClearsTheCount proves a working client that mistypes a few
// times is not locked out later for those old failures.
func TestBasicAuthSuccessClearsTheCount(t *testing.T) {
	ts, calls := throttledServer(t)

	failUntilLocked(t, ts, testUser, authlimit.DefaultMaxFails-1)
	if code := attempt(t, ts, testUser, testPass); code == http.StatusTooManyRequests || code == http.StatusUnauthorized {
		t.Fatalf("the correct password below the cap = %d, want it served", code)
	}
	failUntilLocked(t, ts, testUser, authlimit.DefaultMaxFails-1)
	before := *calls
	if code := attempt(t, ts, testUser, testPass); code == http.StatusTooManyRequests || code == http.StatusUnauthorized {
		t.Errorf("a successful login did not clear the failure count: status %d", code)
	}
	if *calls != before+1 {
		t.Errorf("the password was checked %d times, want 1", *calls-before)
	}
}
