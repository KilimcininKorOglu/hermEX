package mta

import (
	"testing"

	"hermex/internal/authlimit"
	"hermex/internal/directory"
)

// countingAuth counts how often a password is actually checked: each check runs
// the directory's 600k-round hash, which is the cost the throttle exists to stop.
type countingAuth struct {
	directory.StaticAccounts
	calls *int
}

func (c countingAuth) Authenticate(user, pass string) (string, bool) {
	*c.calls++
	return c.StaticAccounts.Authenticate(user, pass)
}

// authSession builds one submission session from the given client address.
func authSession(t *testing.T, b *Backend, remote string) *session {
	t.Helper()
	s, err := b.NewSession(remote)
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := s.(*session)
	if !ok {
		t.Fatalf("session type = %T", s)
	}
	return sess
}

// TestSMTPAuthThrottlesGuessing proves submission cannot be used to guess
// passwords unbounded. It is keyed by client address, like IMAP and POP3: the MTA
// is not behind the gateway, so the address is the real client's.
func TestSMTPAuthThrottlesGuessing(t *testing.T) {
	calls := 0
	accounts := countingAuth{
		StaticAccounts: directory.StaticAccounts{
			"alice@acme.test": {Password: "secret", MailboxPath: t.TempDir()},
		},
		calls: &calls,
	}
	b := &Backend{Accounts: accounts, Limiter: authlimit.New(0, 0, 0)}

	for i := range authlimit.DefaultMaxFails {
		if authSession(t, b, "198.51.100.7:2525").Auth("alice@acme.test", "wrong") {
			t.Fatalf("attempt %d was accepted", i)
		}
	}
	before := calls
	if authSession(t, b, "198.51.100.7:2525").Auth("alice@acme.test", "secret") {
		t.Error("the correct password was accepted while the client was locked out")
	}
	if calls != before {
		t.Errorf("the password was checked %d more times while locked out", calls-before)
	}

	// A different client is unaffected: the key is the address, and one guesser
	// must not lock the deployment out.
	if !authSession(t, b, "203.0.113.9:2525").Auth("alice@acme.test", "secret") {
		t.Error("another client address was locked out too")
	}
}

// TestSMTPAuthWithoutLimiterIsUnchanged covers a backend with no limiter wired:
// the behaviour must be exactly as before.
func TestSMTPAuthWithoutLimiterIsUnchanged(t *testing.T) {
	accounts := directory.StaticAccounts{
		"alice@acme.test": {Password: "secret", MailboxPath: t.TempDir()},
	}
	b := &Backend{Accounts: accounts}

	for range authlimit.DefaultMaxFails + 3 {
		authSession(t, b, "198.51.100.7:2525").Auth("alice@acme.test", "wrong")
	}
	if !authSession(t, b, "198.51.100.7:2525").Auth("alice@acme.test", "secret") {
		t.Error("a backend with no limiter refused a correct password")
	}
}
