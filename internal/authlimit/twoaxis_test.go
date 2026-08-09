package authlimit

import (
	"fmt"
	"testing"
	"time"
)

// TestDistributedGuessingTripsTheAccountAxis proves a guesser who rotates source
// addresses still runs into the target account's counter. An address-only limiter
// gives every new address a fresh budget, so the account it is aimed at is never
// protected at all.
func TestDistributedGuessingTripsTheAccountAxis(t *testing.T) {
	l := New(3, time.Minute, time.Minute)
	l.now = func() time.Time { return time.Unix(0, 0) }

	const victim = "victim@hermex.test"
	for i := range 3 {
		addr := fmt.Sprintf("198.51.100.%d:4000", i+1) // a different host every time
		if !l.Allowed(addr, victim) {
			t.Fatalf("attempt %d refused early", i)
		}
		l.Fail(addr, victim)
	}
	if l.Allowed("203.0.113.77:4000", victim) {
		t.Error("the account is still admitted from a fresh address after reaching its threshold")
	}
	// The individual addresses are not locked: one failure each is far below the
	// address threshold, so an innocent host that shares an address is unaffected.
	if !l.Allowed("198.51.100.1:4000", "someone.else@hermex.test") {
		t.Error("an address with a single failure was locked out")
	}
}

// TestSprayingTripsTheAddressAxis proves one host working through many accounts is
// caught by the address counter. An account-only limiter never sees it: no single
// account reaches its threshold.
func TestSprayingTripsTheAddressAxis(t *testing.T) {
	l := New(3, time.Minute, time.Minute)
	l.now = func() time.Time { return time.Unix(0, 0) }

	const attacker = "198.51.100.7:5000"
	limit := 3 * ipFailFactor
	for i := range limit - 1 {
		acct := fmt.Sprintf("user%d@hermex.test", i)
		if !l.Allowed(attacker, acct) {
			t.Fatalf("attempt %d refused before the address threshold", i)
		}
		l.Fail(attacker, acct)
	}
	// Negative control: one short of the threshold the host is still served, so the
	// address axis is not simply refusing everything.
	if !l.Allowed(attacker, "fresh@hermex.test") {
		t.Fatalf("the address was locked out after %d failures, want %d", limit-1, limit)
	}
	l.Fail(attacker, "fresh@hermex.test")
	if l.Allowed(attacker, "another@hermex.test") {
		t.Errorf("the address is still admitted after %d failures across accounts", limit)
	}
}

// TestTheAddressAxisIsLooserThanTheAccountAxis states the ratio directly. One
// address is many people (an office behind a NAT, a carrier gateway), so locking a
// whole site out over a handful of typos would be worse than the attack.
func TestTheAddressAxisIsLooserThanTheAccountAxis(t *testing.T) {
	l := New(2, time.Minute, time.Minute)
	l.now = func() time.Time { return time.Unix(0, 0) }

	const office = "198.51.100.20:6000"
	for i := range 2 {
		l.Fail(office, fmt.Sprintf("colleague%d@hermex.test", i))
	}
	if !l.Allowed(office, "colleague9@hermex.test") {
		t.Error("the shared address was locked out at the account threshold, not its own")
	}
}

// TestTheTwoAxesDoNotShareCounters proves an account that reads like an address
// cannot collide with that address's counter, which would let a lockout on one
// silently lock the other.
func TestTheTwoAxesDoNotShareCounters(t *testing.T) {
	l := New(2, time.Minute, time.Minute)
	l.now = func() time.Time { return time.Unix(0, 0) }

	l.Fail("", "10.0.0.1")
	l.Fail("", "10.0.0.1") // the account named like an address is now locked
	if l.Allowed("", "10.0.0.1") {
		t.Fatal("the account was not locked out")
	}
	if !l.Allowed("10.0.0.1:1234", "someone@hermex.test") {
		t.Error("the address counter was locked by an account of the same name")
	}
}

// TestSucceedClearsBothAxes proves a correct password clears the attempt's own two
// counters and nothing else, so one user's success cannot launder another's.
func TestSucceedClearsBothAxes(t *testing.T) {
	l := New(2, time.Minute, time.Minute)
	l.now = func() time.Time { return time.Unix(0, 0) }

	const addr, acct = "198.51.100.5:7000", "alice@hermex.test"
	l.Fail(addr, acct)
	l.Fail("", "bob@hermex.test")
	l.Fail("", "bob@hermex.test") // bob is locked out
	l.Succeed(addr, acct)

	l.Fail(addr, acct)
	if !l.Allowed(addr, acct) {
		t.Error("the successful login's own counters were not cleared")
	}
	if l.Allowed("", "bob@hermex.test") {
		t.Error("one account's success cleared another account's lockout")
	}
}
