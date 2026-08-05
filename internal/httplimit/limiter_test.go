package httplimit

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// newTestLimiter builds an enabled limiter with a controllable clock, so a window can
// be advanced without sleeping.
func newTestLimiter(t *testing.T, burst int, period time.Duration) (*Limiter, func(time.Duration)) {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	l := NewLimiter()
	l.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	l.SetLimits(burst, period)
	l.SetEnabled(true)
	return l, func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}
}

// TestAllowAdmitsUpToBurst proves the limiter admits exactly the burst within one
// window and refuses the next request, and that the refusal reports the time left in
// that window so a client that waits is admitted.
func TestAllowAdmitsUpToBurst(t *testing.T) {
	l, advance := newTestLimiter(t, 3, time.Minute)
	for i := range 3 {
		if allowed, _ := l.Allow("10.0.0.1"); !allowed {
			t.Fatalf("request %d refused, want admitted within burst", i+1)
		}
	}
	allowed, retry := l.Allow("10.0.0.1")
	if allowed {
		t.Fatal("request past the burst was admitted, want refused")
	}
	if retry != time.Minute {
		t.Errorf("retryAfter = %v, want the full window (%v)", retry, time.Minute)
	}

	// A different client keeps its own budget.
	if allowed, _ := l.Allow("10.0.0.2"); !allowed {
		t.Error("a second client was refused, want its own budget")
	}

	// Partway through the window the retry shrinks to what is left.
	advance(40 * time.Second)
	if _, retry := l.Allow("10.0.0.1"); retry != 20*time.Second {
		t.Errorf("retryAfter mid-window = %v, want 20s", retry)
	}

	// Once the window elapses the counter resets.
	advance(21 * time.Second)
	if allowed, _ := l.Allow("10.0.0.1"); !allowed {
		t.Error("refused after the window elapsed, want a fresh budget")
	}
}

// TestAllowNeverAdvertisesZeroRetry proves a refusal at the very end of a window still
// tells the client to wait at least a second, so it does not retry into the same
// refusal immediately.
func TestAllowNeverAdvertisesZeroRetry(t *testing.T) {
	l, advance := newTestLimiter(t, 1, time.Minute)
	l.Allow("10.0.0.1")
	advance(59*time.Second + 900*time.Millisecond)
	_, retry := l.Allow("10.0.0.1")
	if retry < time.Second {
		t.Errorf("retryAfter = %v, want at least 1s", retry)
	}
}

// TestDisabledAdmitsEverything proves the limiter is inert until an operator enables
// it: the default state must not throttle any client.
func TestDisabledAdmitsEverything(t *testing.T) {
	l := NewLimiter()
	if l.Enabled() {
		t.Fatal("a new limiter is enabled, want disabled until an operator turns it on")
	}
	l.SetLimits(1, time.Minute)
	for i := range 50 {
		if allowed, _ := l.Allow("10.0.0.1"); !allowed {
			t.Fatalf("request %d refused while disabled, want admitted", i+1)
		}
	}
}

// TestAllowFailsOpenOnEmptyKey proves an unkeyable client is admitted rather than
// counted against a shared bucket.
func TestAllowFailsOpenOnEmptyKey(t *testing.T) {
	l, _ := newTestLimiter(t, 1, time.Minute)
	for i := range 10 {
		if allowed, _ := l.Allow(""); !allowed {
			t.Fatalf("request %d with an empty key refused, want fail open", i+1)
		}
	}
}

// TestAllowFailsOpenWhenTableFull proves a table full of live windows admits a new
// client rather than evicting a live counter: reclaiming memory must never turn into
// blocking a legitimate client.
func TestAllowFailsOpenWhenTableFull(t *testing.T) {
	l, _ := newTestLimiter(t, 1, time.Minute)
	for i := range maxKeys {
		l.Allow("live-" + strconv.Itoa(i))
	}
	// Every window is live, so the newcomer cannot be tracked and must be admitted.
	for i := range 5 {
		if allowed, _ := l.Allow("newcomer"); !allowed {
			t.Fatalf("request %d refused with a full table, want fail open", i+1)
		}
	}
}

// TestAllowReclaimsExpiredWhenTableFull proves a full table of elapsed windows is swept
// so a newcomer is tracked and limited normally.
func TestAllowReclaimsExpiredWhenTableFull(t *testing.T) {
	l, advance := newTestLimiter(t, 1, time.Minute)
	for i := range maxKeys {
		l.Allow("old-" + strconv.Itoa(i))
	}
	advance(2 * time.Minute)
	if allowed, _ := l.Allow("newcomer"); !allowed {
		t.Fatal("newcomer refused, want admitted into a swept table")
	}
	if allowed, _ := l.Allow("newcomer"); allowed {
		t.Error("newcomer's second request admitted, want it tracked and refused past the burst")
	}
}

// TestPruneDropsElapsedWindows proves the periodic prune frees elapsed windows and
// keeps live ones.
func TestPruneDropsElapsedWindows(t *testing.T) {
	l, advance := newTestLimiter(t, 5, time.Minute)
	l.Allow("old")
	advance(2 * time.Minute)
	l.Allow("fresh")
	l.Prune()
	l.mu.Lock()
	_, oldTracked := l.windows["old"]
	_, freshTracked := l.windows["fresh"]
	l.mu.Unlock()
	if oldTracked {
		t.Error("an elapsed window survived Prune")
	}
	if !freshTracked {
		t.Error("a live window was dropped by Prune")
	}
}

// TestSetLimitsRejectsNonsense proves a burst below 1 or a non-positive window is
// ignored, so a bad stored value can never configure the limiter to admit nothing.
func TestSetLimitsRejectsNonsense(t *testing.T) {
	l := NewLimiter()
	l.SetLimits(0, 0)
	if l.Burst() != defaultBurst || l.Period() != defaultWindow {
		t.Errorf("limits = %d/%v, want the defaults %d/%v", l.Burst(), l.Period(), defaultBurst, defaultWindow)
	}
	l.SetLimits(-5, -time.Second)
	if l.Burst() != defaultBurst || l.Period() != defaultWindow {
		t.Errorf("limits after negatives = %d/%v, want the defaults", l.Burst(), l.Period())
	}
}
