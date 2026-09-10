package connlimit

import "testing"

// enabled builds a limiter that is on with the given caps.
func enabled(t *testing.T, total, perClient int) *Limiter {
	t.Helper()
	l := New()
	l.SetLimits(total, perClient)
	l.SetEnabled(true)
	return l
}

// acquire takes a slot and fails the test when the answer is not what was wanted.
func acquire(t *testing.T, l *Limiter, key string, want bool) func() {
	t.Helper()
	release, ok, why := l.Acquire(key)
	if ok != want {
		t.Fatalf("Acquire(%q) = %v (why %q), want %v", key, ok, why, want)
	}
	return release
}

// TestDisabledAdmitsEverything proves the cap does nothing until an operator turns
// it on, which is how it ships.
func TestDisabledAdmitsEverything(t *testing.T) {
	l := New()
	l.SetLimits(1, 1)
	for i := range 5 {
		if release, ok, _ := l.Acquire("10.0.0.1"); !ok {
			t.Fatalf("connection %d refused while the cap is off", i)
		} else {
			release()
		}
	}
}

// TestTotalCapRefuses is the daemon-wide case: once the daemon holds its budget,
// the next connection is refused whoever it comes from.
func TestTotalCapRefuses(t *testing.T) {
	l := enabled(t, 2, 100)
	acquire(t, l, "10.0.0.1", true)
	acquire(t, l, "10.0.0.2", true)

	_, ok, why := l.Acquire("10.0.0.3")
	if ok || why != ReasonTotal {
		t.Errorf("third connection = %v (why %q), want refused by the total cap", ok, why)
	}
}

// TestPerClientCapRefusesOnlyThatClient is the point of the per-address axis: one
// client filling its own share must not lock everybody else out.
func TestPerClientCapRefusesOnlyThatClient(t *testing.T) {
	l := enabled(t, 100, 2)
	acquire(t, l, "10.0.0.1", true)
	acquire(t, l, "10.0.0.1", true)

	_, ok, why := l.Acquire("10.0.0.1")
	if ok || why != ReasonPerClient {
		t.Errorf("third connection from one client = %v (why %q), want refused by the per-client cap", ok, why)
	}
	acquire(t, l, "10.0.0.2", true) // another client is unaffected
}

// TestReleaseFreesTheSlot proves a closed connection gives its slot back, so a
// busy daemon recovers instead of staying full for its lifetime.
func TestReleaseFreesTheSlot(t *testing.T) {
	l := enabled(t, 1, 1)
	release := acquire(t, l, "10.0.0.1", true)
	acquire(t, l, "10.0.0.1", false)

	release()
	acquire(t, l, "10.0.0.1", true)
}

// TestDoubleReleaseKeepsTheCount proves releasing twice cannot drive the counters
// below zero, which would let the daemon admit more than its cap forever after.
func TestDoubleReleaseKeepsTheCount(t *testing.T) {
	l := enabled(t, 2, 2)
	release := acquire(t, l, "10.0.0.1", true)
	release()
	release()

	if total, perClient := l.InUse("10.0.0.1"); total != 0 || perClient != 0 {
		t.Fatalf("in use after a double release = %d/%d, want 0/0", total, perClient)
	}
	acquire(t, l, "10.0.0.1", true)
	acquire(t, l, "10.0.0.1", true)
	acquire(t, l, "10.0.0.1", false)
}

// TestEmptyKeyCountsAgainstTheTotal proves a connection whose address cannot be
// read is still bounded by something.
func TestEmptyKeyCountsAgainstTheTotal(t *testing.T) {
	l := enabled(t, 1, 1)
	acquire(t, l, "", true)

	_, ok, why := l.Acquire("")
	if ok || why != ReasonTotal {
		t.Errorf("second unkeyed connection = %v (why %q), want refused by the total cap", ok, why)
	}
}

// TestSetLimitsIgnoresAnImpossibleValue proves the limiter is never configured to
// admit no connection at all.
func TestSetLimitsIgnoresAnImpossibleValue(t *testing.T) {
	l := enabled(t, 50, 5)
	l.SetLimits(0, -1)

	if l.MaxTotal() != 50 || l.MaxPerClient() != 5 {
		t.Errorf("caps = %d/%d, want the previous 50/5", l.MaxTotal(), l.MaxPerClient())
	}
}
