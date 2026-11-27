package activesync

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
	srv := NewServer(accs, accs, "mail.hermex.test")
	srv.Limiter = authlimit.New(0, 0, 0)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, &calls
}

// attempt makes one Basic-Auth request and returns its status.
func attempt(t *testing.T, ts *httptest.Server, user, pass string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodOptions, ts.URL+"/Microsoft-Server-ActiveSync", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

// failTimes makes n attempts with a wrong password and requires each to be
// refused as unauthenticated, so the lockout a test builds on is real.
func failTimes(t *testing.T, ts *httptest.Server, user string, n int) {
	t.Helper()
	for i := range n {
		if code := attempt(t, ts, user, "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("wrong-password attempt %d status = %d, want 401", i, code)
		}
	}
}

// wantStatus fails the test when one attempt does not answer want.
func wantStatus(t *testing.T, ts *httptest.Server, user, pass string, want int) {
	t.Helper()
	if code := attempt(t, ts, user, pass); code != want {
		t.Errorf("%s status = %d, want %d", user, code, want)
	}
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

// TestBasicAuthThrottleIgnoresAddressCase proves the lockout follows the account,
// not the spelling of its address. An address compares case-insensitively, so a
// guesser who rewrites the case after the lockout must still be refused rather
// than handed a fresh budget of attempts.
func TestBasicAuthThrottleIgnoresAddressCase(t *testing.T) {
	ts, calls := throttledServer(t)
	failTimes(t, ts, testUser, authlimit.DefaultMaxFails)
	before := *calls
	if code := attempt(t, ts, strings.ToUpper(testUser), "wrong"); code != http.StatusTooManyRequests {
		t.Errorf("status for the upper-case address after the lockout = %d, want 429", code)
	}
	if *calls != before {
		t.Error("the upper-case address had its password checked while the account was locked out")
	}
}

// TestBasicAuthThrottleIsPerAccount proves one account's lockout does not take
// the whole deployment down with it.
func TestBasicAuthThrottleIsPerAccount(t *testing.T) {
	ts, _ := throttledServer(t)

	failTimes(t, ts, testUser, authlimit.DefaultMaxFails)
	wantStatus(t, ts, testUser, "wrong", http.StatusTooManyRequests)
	// The other account still signs in: OPTIONS with valid credentials is 200.
	wantStatus(t, ts, "bob@hermex.test", testPass, http.StatusOK)
}

// unknownPrivileges authenticates the password but reports the account unknown
// to the privilege lookup, while the privileges it returns beside that answer
// would permit every service.
type unknownPrivileges struct{ directory.StaticAccounts }

func (unknownPrivileges) Privileges(string) (directory.ServicePrivileges, bool) {
	return directory.ServicePrivileges{EAS: true, Web: true, DAV: true, SMTP: true, POP3IMAP: true}, false
}

// TestBasicAuthRefusesAnAccountWithUnknownPrivileges keeps the service gate
// fail-closed: the Authenticator contract says ok=false means the directory does
// not know the user, so whatever privileges came with it grant nothing.
func TestBasicAuthRefusesAnAccountWithUnknownPrivileges(t *testing.T) {
	accs := unknownPrivileges{directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: t.TempDir()}}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	wantStatus(t, ts, testUser, testPass, http.StatusForbidden)
}

// TestBasicAuthSuccessClearsTheCount proves a working client that mistypes a few
// times is not locked out later for those old failures.
func TestBasicAuthSuccessClearsTheCount(t *testing.T) {
	ts, _ := throttledServer(t)

	failTimes(t, ts, testUser, authlimit.DefaultMaxFails-1)
	wantStatus(t, ts, testUser, testPass, http.StatusOK)
	failTimes(t, ts, testUser, authlimit.DefaultMaxFails-1)
	wantStatus(t, ts, testUser, testPass, http.StatusOK)
}
