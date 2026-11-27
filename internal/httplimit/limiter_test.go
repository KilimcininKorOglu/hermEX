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

// wantAdmitted fails the test unless a request from key is admitted with no retry
// advertised, which Allow's contract makes zero for every admitted request.
func wantAdmitted(t *testing.T, l *Limiter, key, what string) {
	t.Helper()
	if allowed, retry := l.Allow(key); !allowed || retry != 0 {
		t.Errorf("%s: Allow(%q) = %v, %v; want admitted with no retry", what, key, allowed, retry)
	}
}

// wantRefused fails the test unless a request from key is refused with a retry of
// at least one second.
func wantRefused(t *testing.T, l *Limiter, key, what string) {
	t.Helper()
	if allowed, retry := l.Allow(key); allowed || retry < time.Second {
		t.Errorf("%s: Allow(%q) = %v, %v; want refused with a retry", what, key, allowed, retry)
	}
}

// TestAllowAdmitsUpToBurst proves the limiter admits exactly the burst within one
// window and refuses the next request, and that the refusal reports the time left in
// that window so a client that waits is admitted.
func TestAllowAdmitsUpToBurst(t *testing.T) {
	l, advance := newTestLimiter(t, 3, time.Minute)
	for i := range 3 {
		wantAdmitted(t, l, "10.0.0.1", "request "+strconv.Itoa(i+1)+" within the burst")
	}
	allowed, retry := l.Allow("10.0.0.1")
	if allowed {
		t.Fatal("request past the burst was admitted, want refused")
	}
	if retry != time.Minute {
		t.Errorf("retryAfter = %v, want the full window (%v)", retry, time.Minute)
	}

	// A different client keeps its own budget.
	wantAdmitted(t, l, "10.0.0.2", "a second client with its own budget")

	// Partway through the window the retry shrinks to what is left.
	advance(40 * time.Second)
	if allowed, retry := l.Allow("10.0.0.1"); allowed || retry != 20*time.Second {
		t.Errorf("mid-window = %v, %v; want refused with 20s left", allowed, retry)
	}

	// Once the window elapses the counter resets.
	advance(21 * time.Second)
	wantAdmitted(t, l, "10.0.0.1", "the first request of a fresh window")
}

// TestAllowNeverAdvertisesZeroRetry proves a refusal at the very end of a window still
// tells the client to wait at least a second, so it does not retry into the same
// refusal immediately.
func TestAllowNeverAdvertisesZeroRetry(t *testing.T) {
	l, advance := newTestLimiter(t, 1, time.Minute)
	wantAdmitted(t, l, "10.0.0.1", "the only request of the burst")
	advance(59*time.Second + 900*time.Millisecond)
	wantRefused(t, l, "10.0.0.1", "a request 100ms before the window ends")
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
		wantAdmitted(t, l, "10.0.0.1", "request "+strconv.Itoa(i+1)+" while disabled")
	}
}

// TestAllowFailsOpenOnEmptyKey proves an unkeyable client is admitted rather than
// counted against a shared bucket.
func TestAllowFailsOpenOnEmptyKey(t *testing.T) {
	l, _ := newTestLimiter(t, 1, time.Minute)
	for i := range 10 {
		wantAdmitted(t, l, "", "request "+strconv.Itoa(i+1)+" with an empty key")
	}
}

// TestAFullTableEvictsTheLeastRecentlyUsedClient fills the table with live
// windows, the state a client spreading requests over many addresses (an IPv6
// /48 alone holds 65536 /64 keys) can reach. Admitting every newcomer untracked
// would switch the limit off for everyone, so the table instead drops the client
// that has gone longest without a request, as nginx limit_req does with a full
// zone: the newcomer is tracked and limited, a recently active client keeps its
// count, and only the evicted one starts over.
func TestAFullTableEvictsTheLeastRecentlyUsedClient(t *testing.T) {
	l, _ := newTestLimiter(t, 1, time.Minute)
	for i := range maxKeys {
		l.Allow("live-" + strconv.Itoa(i))
	}
	// live-0 was the first in, but this request makes it the most recently used.
	wantRefused(t, l, "live-0", "live-0's second request")

	wantAdmitted(t, l, "newcomer", "newcomer's first request")
	wantRefused(t, l, "newcomer", "newcomer's second request, tracked")
	wantRefused(t, l, "live-0", "live-0, the most recently used client")
	if got := len(l.windows); got != maxKeys {
		t.Errorf("table holds %d windows, want it kept at %d", got, maxKeys)
	}
	// live-1 was the least recently used, so it is the one that starts over.
	wantAdmitted(t, l, "live-1", "live-1, the evicted least recently used client")
}

// TestAllowReclaimsExpiredWhenTableFull proves a full table of elapsed windows is swept
// so a newcomer is tracked and limited normally.
func TestAllowReclaimsExpiredWhenTableFull(t *testing.T) {
	l, advance := newTestLimiter(t, 1, time.Minute)
	for i := range maxKeys {
		l.Allow("old-" + strconv.Itoa(i))
	}
	advance(2 * time.Minute)
	wantAdmitted(t, l, "newcomer", "newcomer into a swept table")
	wantRefused(t, l, "newcomer", "newcomer's second request, tracked")
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
	if l.Burst() != DefaultBurst || l.Period() != DefaultWindow {
		t.Errorf("limits = %d/%v, want the defaults %d/%v", l.Burst(), l.Period(), DefaultBurst, DefaultWindow)
	}
	l.SetLimits(-5, -time.Second)
	if l.Burst() != DefaultBurst || l.Period() != DefaultWindow {
		t.Errorf("limits after negatives = %d/%v, want the defaults", l.Burst(), l.Period())
	}
}
