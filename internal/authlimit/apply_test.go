package authlimit

import (
	"errors"
	"testing"
	"time"
)

// stored builds a Reader over a fixed settings row.
func stored(s Settings) Reader {
	return func() (Settings, bool, error) { return s, true, nil }
}

// TestApplyRetunesARunningLimiter is the whole point of this path. The tuning used
// to live in package constants and every call site passed zeros, so an operator
// facing a credential-stuffing wave could not tighten the threshold, and one facing
// a lockout storm hitting legitimate users could not loosen it, without editing the
// source and rebuilding the daemon.
func TestApplyRetunesARunningLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(0, 0, 0)
	l.now = func() time.Time { return now }

	Apply("test", nil, l, stored(Settings{MaxFails: 2, WindowSeconds: 60, LockoutSeconds: 300}))
	if l.MaxFails() != 2 || l.Window() != time.Minute || l.Lockout() != 5*time.Minute {
		t.Fatalf("tuning = %d/%s/%s, want 2/1m0s/5m0s", l.MaxFails(), l.Window(), l.Lockout())
	}

	// The new threshold governs an actual lockout, not just the reported numbers.
	const key = "1.2.3.4"
	l.Fail(key)
	if !l.Allowed(key) {
		t.Fatal("one failure locked the key out under a threshold of two")
	}
	l.Fail(key)
	if l.Allowed(key) {
		t.Fatal("the tightened threshold was reported but not enforced")
	}
	now = now.Add(5*time.Minute + time.Second)
	if !l.Allowed(key) {
		t.Error("the key is still locked after the configured lockout elapsed")
	}
}

// TestApplyLoosensAsWellAsTightens covers the direction an operator reaches for
// during an outage: legitimate users are locking themselves out and the threshold
// has to go up on a daemon that is already serving.
func TestApplyLoosensAsWellAsTightens(t *testing.T) {
	l := New(2, time.Minute, time.Minute)
	l.now = func() time.Time { return time.Unix(0, 0) }

	const key = "10.0.0.1"
	l.Fail(key)
	l.Fail(key)
	if l.Allowed(key) {
		t.Fatal("the key was not locked out to begin with")
	}

	Apply("test", nil, l, stored(Settings{MaxFails: 10, WindowSeconds: 60, LockoutSeconds: 60}))
	// The existing lockout stands (it was earned under the old rule), but the next
	// window counts to the new threshold.
	l.Succeed(key)
	for i := range 9 {
		l.Fail(key)
		if !l.Allowed(key) {
			t.Fatalf("failure %d locked the key out under a threshold of ten", i+1)
		}
	}
}

// TestApplyIgnoresNonPositiveValues proves a malformed row cannot lock out every
// login on the daemon. A threshold of zero trips on the first failure, and a
// zero-length window resets nothing.
func TestApplyIgnoresNonPositiveValues(t *testing.T) {
	l := New(5, 15*time.Minute, 15*time.Minute)
	Apply("test", nil, l, stored(Settings{MaxFails: 0, WindowSeconds: -1, LockoutSeconds: 0}))
	if l.MaxFails() != 5 || l.Window() != 15*time.Minute || l.Lockout() != 15*time.Minute {
		t.Errorf("a zeroed row retuned the limiter to %d/%s/%s", l.MaxFails(), l.Window(), l.Lockout())
	}
}

// TestApplyKeepsTheCurrentTuningOnFailure proves a database hiccup does not snap
// the limiter back to the built-in defaults, which would silently undo an
// operator's tightening exactly when the database is unhappy.
func TestApplyKeepsTheCurrentTuningOnFailure(t *testing.T) {
	l := New(0, 0, 0)
	Apply("test", nil, l, stored(Settings{MaxFails: 3, WindowSeconds: 60, LockoutSeconds: 60}))

	Apply("test", nil, l, func() (Settings, bool, error) { return Settings{}, false, errors.New("db down") })
	if l.MaxFails() != 3 {
		t.Errorf("a read error reset the threshold to %d", l.MaxFails())
	}
	Apply("test", nil, l, func() (Settings, bool, error) { return Settings{}, false, nil })
	if l.MaxFails() != 3 {
		t.Errorf("an empty settings table reset the threshold to %d", l.MaxFails())
	}
}

// TestApplyWithNoRowKeepsTheDefaults proves an install that never opens the page
// behaves exactly as it did before this setting existed.
func TestApplyWithNoRowKeepsTheDefaults(t *testing.T) {
	l := New(0, 0, 0)
	Apply("test", nil, l, func() (Settings, bool, error) { return Settings{}, false, nil })
	if l.MaxFails() != DefaultMaxFails || l.Window() != DefaultWindow || l.Lockout() != DefaultLockout {
		t.Errorf("defaults = %d/%s/%s, want the built-in tuning", l.MaxFails(), l.Window(), l.Lockout())
	}
}

// TestPruneDropsElapsedCountersButNotLockouts proves the periodic sweep bounds the
// tracking table without releasing a key that is still serving its lockout. The
// table otherwise only swept when it hit its 100k cap, at which point it fails open.
func TestPruneDropsElapsedCountersButNotLockouts(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(2, time.Minute, time.Hour)
	l.now = func() time.Time { return now }

	l.Fail("stale")                // one failure, never locked out
	l.Fail("locked")               // ...
	l.Fail("locked")               // two failures: locked out for an hour
	now = now.Add(2 * time.Minute) // the window has elapsed for both
	l.Prune()

	l.mu.Lock()
	_, staleKept := l.attempts["stale"]
	_, lockedKept := l.attempts["locked"]
	l.mu.Unlock()
	if staleKept {
		t.Error("an elapsed counter was kept, so the table grows to its cap and fails open")
	}
	if !lockedKept {
		t.Error("a key still inside its lockout was pruned, releasing it early")
	}
	if l.Allowed("locked") {
		t.Error("the pruned key is admitted despite its lockout")
	}
}
