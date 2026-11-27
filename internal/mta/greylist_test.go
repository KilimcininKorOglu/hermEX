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
	m          map[string]directory.Greylisted
	getErr     error
	upsertErr  error
	confirmErr error
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
	if g.upsertErr != nil {
		return g.upsertErr
	}
	cur, ok := g.m[glKey(ip, s, r)]
	if !ok {
		cur = directory.Greylisted{FirstSeen: now}
	}
	g.m[glKey(ip, s, r)] = cur
	return nil
}
func (g *memGreylist) GreylistConfirm(ip, s, r string, now int64) error {
	if g.confirmErr != nil {
		return g.confirmErr
	}
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
// passes, proving the triplet keys on the network, not the exact IP (the classic
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
	now += int64(defaultGreylistMinDelay.Seconds()) + 1
	if !g.ShouldDefer(net.IPv4(203, 0, 114, 5), "s@ext.example", "u@local.example") {
		t.Error("a different /24 must still defer as a new triplet")
	}
	if g.ShouldDefer(net.IPv4(203, 0, 113, 99), "S@EXT.EXAMPLE", "U@LOCAL.EXAMPLE") {
		t.Error("a retry from the same /24 (different IP, mixed case) after the delay must pass, not defer")
	}
}

// TestGreylistSetTimings proves the accept delay is read from the runtime timings, so
// an operator's edit (here a longer minimum delay) decides when a retry is accepted,
// with no restart: a retry past the built-in default but short of the configured delay
// still defers, and one past the configured delay passes.
func TestGreylistSetTimings(t *testing.T) {
	store := &memGreylist{m: map[string]directory.Greylisted{}}
	g := NewGreylister(store, nil)
	g.SetEnabled(true)
	g.SetTimings(600, 0, 0) // 10-minute minimum delay; leave the TTLs unchanged
	g.SetTimings(0, -1, -5) // non-positive timings leave current values unchanged
	now := int64(1000)
	g.now = func() int64 { return now }

	if !g.ShouldDefer(net.IPv4(203, 0, 113, 5), "s@ext.example", "u@local.example") {
		t.Fatal("first contact should be deferred")
	}
	now += int64(defaultGreylistMinDelay.Seconds()) + 1 // past the 5-minute default...
	if !g.ShouldDefer(net.IPv4(203, 0, 113, 6), "s@ext.example", "u@local.example") {
		t.Error("a retry short of the configured 10-minute delay must still defer")
	}
	now += 300 // ...now past the configured 10 minutes total
	if g.ShouldDefer(net.IPv4(203, 0, 113, 7), "s@ext.example", "u@local.example") {
		t.Error("a retry past the configured delay must pass, not defer")
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

	if !g.ShouldDefer(net.IPv4(10, 0, 0, 1), "s@ext", "u@local") {
		t.Fatal("first contact should defer")
	}
	now += 60 // less than the default min delay
	if !g.ShouldDefer(net.IPv4(10, 0, 0, 2), "s@ext", "u@local") {
		t.Error("a retry before the minimum delay must still be deferred")
	}
}

// TestGreylistFailOpenOnStoreError proves a store error accepts the mail rather than
// deferring it, greylisting never loses mail on its own failure, and nil IP fails open.
func TestGreylistFailOpenOnStoreError(t *testing.T) {
	sink := withPassLogger(t)
	g := NewGreylister(&memGreylist{getErr: errors.New("db down")}, nil)
	g.SetEnabled(true)
	if g.ShouldDefer(net.IPv4(1, 2, 3, 4), "s@ext", "u@local") {
		t.Error("a store error must fail open (accept), not defer")
	}
	e, ok := sink.find("greylist.store.fail")
	if !ok || e.Fields["op"] != "get" || e.Err != "db down" {
		t.Errorf("the failed lookup was not recorded as greylist.store.fail op=get: %+v (ok=%v)", e, ok)
	}
	gNil := NewGreylister(&memGreylist{m: map[string]directory.Greylisted{}}, nil)
	gNil.SetEnabled(true)
	if gNil.ShouldDefer(nil, "s@ext", "u@local") {
		t.Error("nil IP must fail open")
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
	if err := sess.Mail("bob@ext.example", smtp.MailParams{}); err != nil {
		t.Fatal(err)
	}
	err = sess.Rcpt("alice@test", smtp.RcptParams{})
	if _, ok := errors.AsType[*smtp.TempError](err); !ok {
		t.Fatalf("first contact should defer with a TempError, got %v", err)
	}
	wantEq(t, "targets after greylist deferral", len(sess.(*session).targets), 0)
}

// TestGreylistRecordsStoreWriteFailures proves store write errors during refresh
// and confirmation are recorded rather than silently dropped while still admitting.
func TestGreylistRecordsStoreWriteFailures(t *testing.T) {
	sink := withPassLogger(t)
	store := &memGreylist{
		m: map[string]directory.Greylisted{
			glKey("203.0.113.0/24", "s@ext.example", "u@local.example"):  {FirstSeen: 100, Confirmed: true},
			glKey("198.51.100.0/24", "s@ext.example", "u@local.example"): {FirstSeen: 100, Confirmed: false},
		},
		upsertErr:  errors.New("refresh failed"),
		confirmErr: errors.New("confirm failed"),
	}
	g := NewGreylister(store, nil)
	g.SetEnabled(true)
	g.now = func() int64 { return 100 + int64(defaultGreylistMinDelay.Seconds()) + 1 }

	if g.ShouldDefer(net.IPv4(203, 0, 113, 5), "s@ext.example", "u@local.example") {
		t.Error("confirmed triplet must pass even when refresh write fails")
	}
	if g.ShouldDefer(net.IPv4(198, 51, 100, 5), "s@ext.example", "u@local.example") {
		t.Error("retried triplet past delay must pass even when confirm write fails")
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	var ops []string
	for _, e := range sink.events {
		if e.Name == "greylist.store.fail" {
			op, ok := e.Fields["op"].(string)
			if !ok {
				t.Fatalf("op field type = %T, want string", e.Fields["op"])
			}
			ops = append(ops, op)
		}
	}
	if len(ops) != 2 || ops[0] != "refresh" || ops[1] != "confirm" {
		t.Errorf("logged greylist store ops = %v, want [refresh confirm]", ops)
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
	if err := cs.Rcpt("alice@test", smtp.RcptParams{}); err != nil {
		t.Fatalf("authenticated submission must not be greylisted: %v", err)
	}
}
