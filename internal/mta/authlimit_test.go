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
// passwords unbounded, and that the account axis holds across client addresses:
// a guesser who rotates source addresses (a botnet, a proxy pool) still runs into
// the target account's own counter.
func TestSMTPAuthThrottlesGuessing(t *testing.T) {
	calls := 0
	accounts := countingAuth{
		StaticAccounts: directory.StaticAccounts{
			"alice@acme.test": {Password: "secret", MailboxPath: t.TempDir()},
			"bob@acme.test":   {Password: "secret", MailboxPath: t.TempDir()},
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

	// Moving to a fresh address does not reset the account's counter: that is the
	// whole point of counting the account as well as the address.
	if authSession(t, b, "203.0.113.9:2525").Auth("alice@acme.test", "secret") {
		t.Error("the locked-out account was admitted from a different address")
	}
	// Another account is unaffected: the address axis is nowhere near its own,
	// larger threshold, so one guesser must not lock the deployment out.
	if !authSession(t, b, "198.51.100.7:2525").Auth("bob@acme.test", "secret") {
		t.Error("an unrelated account was locked out too")
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
