package mta

import (
	"errors"
	"net"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/smtp"
)

// memGreylist is an in-memory triplet store for the greylister unit tests.
type memGreylist struct {
	m      map[string]directory.Greylisted
	getErr error
}

func glKey(ip, s, r string) string { return ip + "|" + s + "|" + r }

func (g *memGreylist) GreylistGet(ip, s, r string) (directory.Greylisted, bool, error) {
	if g.getErr != nil {
		return directory.Greylisted{}, false, g.getErr
	}
	v, ok := g.m[glKey(ip, s, r)]
	return v, ok, nil
}
func (g *memGreylist) GreylistUpsertSeen(ip, s, r string, now int64) error {
	cur, ok := g.m[glKey(ip, s, r)]
	if !ok {
		cur = directory.Greylisted{FirstSeen: now}
	}
	g.m[glKey(ip, s, r)] = cur
	return nil
}
func (g *memGreylist) GreylistConfirm(ip, s, r string, now int64) error {
	cur := g.m[glKey(ip, s, r)]
	cur.Confirmed = true
	g.m[glKey(ip, s, r)] = cur
	return nil
}
func (g *memGreylist) PruneGreylist(int64, int64) error { return nil }

type allowFunc func(mailFrom, fromDomain string) bool

func (f allowFunc) Allowlisted(m, d string) bool { return f(m, d) }

// TestNetworkKey proves a client IP is masked to its network so a different IP in
// the same pool keys identically.
func TestNetworkKey(t *testing.T) {
	if got := networkKey(net.IPv4(203, 0, 113, 5)); got != "203.0.113.0/24" {
		t.Errorf("IPv4 key = %q, want 203.0.113.0/24", got)
	}
	if a, b := networkKey(net.IPv4(203, 0, 113, 5)), networkKey(net.IPv4(203, 0, 113, 99)); a != b {
		t.Errorf("two IPs in the same /24 keyed differently: %q vs %q", a, b)
	}
	if got := networkKey(net.ParseIP("2001:db8::1")); got != "2001:db8::/64" {
		t.Errorf("IPv6 key = %q, want 2001:db8::/64", got)
	}
	if got := networkKey(nil); got != "" {
		t.Errorf("nil IP key = %q, want empty", got)
	}
}

// TestGreylistRetrySameNetworkDifferentIPPasses is the discriminating test: a first
// contact defers, and a retry after the delay from a DIFFERENT IP in the same /24
// passes — proving the triplet keys on the network, not the exact IP (the classic
// greylisting failure with large senders).
func TestGreylistRetrySameNetworkDifferentIPPasses(t *testing.T) {
	store := &memGreylist{m: map[string]directory.Greylisted{}}
	g := NewGreylister(store, nil)
	g.SetEnabled(true)
	now := int64(1000)
	g.now = func() int64 { return now }

	if !g.ShouldDefer(net.IPv4(203, 0, 113, 5), "s@ext.example", "u@local.example") {
		t.Fatal("first contact should be deferred")
	}
	now += int64(greylistMinDelay.Seconds()) + 1
	if g.ShouldDefer(net.IPv4(203, 0, 113, 99), "s@ext.example", "u@local.example") {
		t.Error("a retry from the same /24 (different IP) after the delay must pass, not defer")
	}
}

// TestGreylistRetryTooSoonDefers proves a retry before the minimum delay is still
// deferred.
func TestGreylistRetryTooSoonDefers(t *testing.T) {
	store := &memGreylist{m: map[string]directory.Greylisted{}}
	g := NewGreylister(store, nil)
	g.SetEnabled(true)
	now := int64(1000)
	g.now = func() int64 { return now }

	g.ShouldDefer(net.IPv4(10, 0, 0, 1), "s@ext", "u@local")
	now += 60 // less than greylistMinDelay
	if !g.ShouldDefer(net.IPv4(10, 0, 0, 2), "s@ext", "u@local") {
		t.Error("a retry before the minimum delay must still be deferred")
	}
}

// TestGreylistFailOpenOnStoreError proves a store error accepts the mail rather than
// deferring it — greylisting never loses mail on its own failure.
func TestGreylistFailOpenOnStoreError(t *testing.T) {
	g := NewGreylister(&memGreylist{getErr: errors.New("db down")}, nil)
	g.SetEnabled(true)
	if g.ShouldDefer(net.IPv4(1, 2, 3, 4), "s@ext", "u@local") {
		t.Error("a store error must fail open (accept), not defer")
	}
}

// TestGreylistExemptions proves greylisting is skipped when disabled, for an
// allowlisted sender, and for an empty envelope sender (a bounce).
func TestGreylistExemptions(t *testing.T) {
	store := &memGreylist{m: map[string]directory.Greylisted{}}

	off := NewGreylister(store, nil) // disabled by default
	if off.ShouldDefer(net.IPv4(1, 2, 3, 4), "s@ext", "u@local") {
		t.Error("disabled greylister must accept")
	}

	allowed := NewGreylister(store, allowFunc(func(string, string) bool { return true }))
	allowed.SetEnabled(true)
	if allowed.ShouldDefer(net.IPv4(1, 2, 3, 4), "friend@partner.example", "u@local") {
		t.Error("an allowlisted sender must be exempt from greylisting")
	}

	bounce := NewGreylister(store, nil)
	bounce.SetEnabled(true)
	if bounce.ShouldDefer(net.IPv4(1, 2, 3, 4), "", "u@local") {
		t.Error("an empty envelope sender (a bounce) must be exempt")
	}
}

// TestRcptGreylistDefersUnauthenticated proves the SMTP RCPT path defers a
// first-contact local recipient with a temporary failure.
func TestRcptGreylistDefersUnauthenticated(t *testing.T) {
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: t.TempDir()}}
	g := NewGreylister(&memGreylist{m: map[string]directory.Greylisted{}}, nil)
	g.SetEnabled(true)
	b := &Backend{Accounts: accounts, Greylist: g}

	sess, err := b.NewSession("203.0.113.5:1234")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Mail("bob@ext.example"); err != nil {
		t.Fatal(err)
	}
	err = sess.Rcpt("alice@test")
	var te *smtp.TempError
	if !errors.As(err, &te) {
		t.Fatalf("first contact should defer with a TempError, got %v", err)
	}
}

// TestRcptGreylistSkipsAuthenticated proves authenticated submission is never
// greylisted.
func TestRcptGreylistSkipsAuthenticated(t *testing.T) {
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: t.TempDir()}}
	g := NewGreylister(&memGreylist{m: map[string]directory.Greylisted{}}, nil)
	g.SetEnabled(true)
	b := &Backend{Accounts: accounts, Greylist: g}

	sess, err := b.NewSession("203.0.113.5:1234")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an authenticated submission directly (Mail's send-as check is a
	// separate concern); the greylist gate keys only on s.authUser being set.
	cs := sess.(*session)
	cs.authUser, cs.from = "submitter@test", "bob@ext.example"
	if err := cs.Rcpt("alice@test"); err != nil {
		t.Fatalf("authenticated submission must not be greylisted: %v", err)
	}
}
