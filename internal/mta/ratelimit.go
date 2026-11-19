package mta

import (
	"container/list"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Inbound rate-limit defaults. A fixed window of defaultRateLimitWindow admits up to
// defaultRateLimitBurst messages from one client network; further messages in the
// same window are deferred with a temporary failure so a legitimate server retries
// later. Chosen conservatively and DISABLED by default, an admin opts in and tunes.
const (
	defaultRateLimitWindow = time.Minute
	defaultRateLimitBurst  = 60
)

// rateLimitMaxKeys bounds the limiter's memory: the number of distinct client
// networks tracked at once. When the table is full of still-live windows the
// network that has gone longest without a message is evicted, as nginx limit_req
// does with a full zone. Admitting a newcomer untracked instead would let a sender
// spread over enough networks switch the limit off for everyone; an evicted
// network only starts its count over, and a deferral is temporary in any case.
const rateLimitMaxKeys = 100_000

// rlWindow is one client network's fixed-window counter and its place in the
// recency list.
type rlWindow struct {
	start time.Time
	count int
	elem  *list.Element // Value is the network key
}

// RateLimiter caps how many messages an unauthenticated client network may send in a
// fixed time window, deferring the excess. It is in-process (per MTA), keyed by /24
// (IPv4) or /64 (IPv6) so a sender rotating addresses within a pool shares one
// counter, fails open for a client it cannot key, and is disabled by default.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string]*rlWindow
	recency *list.List   // network keys, most recently used at the front
	burst   atomic.Int64 // max messages per window (admin-tunable)
	window  atomic.Int64 // window length in nanoseconds (admin-tunable)
	enabled atomic.Bool
	now     func() time.Time // injectable clock for tests
}

// NewRateLimiter builds a limiter with the default burst and window; it starts
// disabled.
func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{windows: make(map[string]*rlWindow), recency: list.New(), now: time.Now}
	rl.burst.Store(defaultRateLimitBurst)
	rl.window.Store(int64(defaultRateLimitWindow))
	return rl
}

// SetEnabled turns rate limiting on or off; safe to call concurrently with delivery.
func (rl *RateLimiter) SetEnabled(on bool) { rl.enabled.Store(on) }

// SetLimits sets the burst (max messages per window) and the window length. A burst
// below 1 or a non-positive window is ignored, leaving the current setting, the
// limiter never admits zero messages or collapses the window to nothing.
func (rl *RateLimiter) SetLimits(burst int, window time.Duration) {
	if burst >= 1 {
		rl.burst.Store(int64(burst))
	}
	if window > 0 {
		rl.window.Store(int64(window))
	}
}

// Allow reports whether a message from ip may proceed. It admits (returns true)
// whenever the limiter is off or the IP cannot be keyed; otherwise it counts the
// message in the client network's current window and returns false once that window's
// count has passed the burst.
func (rl *RateLimiter) Allow(ip net.IP) bool {
	if !rl.enabled.Load() {
		return true
	}
	key := networkKey(ip)
	if key == "" {
		return true // cannot key the client → fail open
	}
	burst := rl.burst.Load()
	window := time.Duration(rl.window.Load())
	now := rl.now()

	rl.mu.Lock()
	defer rl.mu.Unlock()
	w := rl.windows[key]
	switch {
	case w == nil:
		w = rl.track(key, now, window)
	case now.Sub(w.start) >= window:
		w.start, w.count = now, 0 // window elapsed → reset
	}
	rl.recency.MoveToFront(w.elem)
	w.count++
	return int64(w.count) <= burst
}

// Prune drops windows whose period has elapsed; the MTA calls it periodically to keep
// the table small.
func (rl *RateLimiter) Prune() {
	window := time.Duration(rl.window.Load())
	now := rl.now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.sweepExpired(now, window)
}

// track starts a window for a new network. A full table first drops the windows
// whose period has elapsed and, when every window is still live, the least
// recently used one. The caller must hold rl.mu.
func (rl *RateLimiter) track(key string, now time.Time, window time.Duration) *rlWindow {
	if len(rl.windows) >= rateLimitMaxKeys && !rl.sweepExpired(now, window) {
		rl.remove(rl.recency.Back())
	}
	w := &rlWindow{start: now, elem: rl.recency.PushFront(key)}
	rl.windows[key] = w
	return w
}

// remove drops one tracked network. The caller must hold rl.mu.
func (rl *RateLimiter) remove(e *list.Element) {
	delete(rl.windows, e.Value.(string))
	rl.recency.Remove(e)
}

// sweepExpired deletes windows whose period has elapsed and reports whether it freed
// at least one slot. The caller must hold rl.mu.
func (rl *RateLimiter) sweepExpired(now time.Time, window time.Duration) bool {
	freed := false
	for _, w := range rl.windows {
		if now.Sub(w.start) >= window {
			rl.remove(w.elem)
			freed = true
		}
	}
	return freed
}
