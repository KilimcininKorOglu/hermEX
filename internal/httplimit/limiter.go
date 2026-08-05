// Package httplimit caps how many HTTP requests one client may issue in a fixed
// time window. It is the request-rate counterpart to the inbound-SMTP limiter in
// internal/mta: the same fixed-window shape, the same fail-open discipline, and
// the same admin-tunable-without-restart contract, applied to every HTTP daemon
// through the shared server base in internal/serve.
package httplimit

import (
	"sync"
	"sync/atomic"
	"time"
)

// Defaults. A fixed window of defaultWindow admits up to defaultBurst requests
// from one client address. The burst is deliberately generous: Outlook, EWS and
// ActiveSync clients issue legitimate bursts during an initial sync, and a limit
// that trips on normal sync traffic would be a wire-compatibility regression.
// Like the inbound-SMTP limiter, this one starts DISABLED so an operator opts in.
const (
	defaultWindow = time.Minute
	defaultBurst  = 600
)

// maxKeys bounds the limiter's memory: the number of distinct client addresses
// tracked at once. When the table is full of still-live windows the limiter fails
// open (admits) rather than evicting a live counter, so reclaiming memory can
// never turn into blocking a legitimate client.
const maxKeys = 100_000

// window is one client's fixed-window counter.
type window struct {
	start time.Time
	count int
}

// Limiter counts requests per client key in a fixed window and reports when a key
// has passed its burst. It is in-process (per daemon), safe for concurrent use,
// disabled until an operator enables it, and fails open on any inability to key
// or track a client.
type Limiter struct {
	mu      sync.Mutex
	windows map[string]*window
	burst   atomic.Int64 // max requests per window (admin-tunable)
	period  atomic.Int64 // window length in nanoseconds (admin-tunable)
	enabled atomic.Bool
	now     func() time.Time // injectable clock for tests
}

// NewLimiter builds a limiter with the default burst and window; it starts
// disabled.
func NewLimiter() *Limiter {
	l := &Limiter{windows: make(map[string]*window), now: time.Now}
	l.burst.Store(defaultBurst)
	l.period.Store(int64(defaultWindow))
	return l
}

// SetEnabled turns rate limiting on or off; safe to call concurrently with
// serving, so an operator's toggle applies without a restart.
func (l *Limiter) SetEnabled(on bool) { l.enabled.Store(on) }

// SetLimits sets the burst (max requests per window) and the window length. A
// burst below 1 or a non-positive window is ignored, leaving the current setting:
// the limiter never admits zero requests or collapses the window to nothing.
func (l *Limiter) SetLimits(burst int, period time.Duration) {
	if burst >= 1 {
		l.burst.Store(int64(burst))
	}
	if period > 0 {
		l.period.Store(int64(period))
	}
}

// Enabled reports whether the limiter is currently on.
func (l *Limiter) Enabled() bool { return l.enabled.Load() }

// Burst reports the current per-window request budget.
func (l *Limiter) Burst() int { return int(l.burst.Load()) }

// Period reports the current window length.
func (l *Limiter) Period() time.Duration { return time.Duration(l.period.Load()) }

// Allow reports whether a request from key may proceed, and how long the caller
// should wait when it may not. It admits whenever the limiter is off or the key
// is empty; otherwise it counts the request in that key's current window and
// refuses once the count has passed the burst. retryAfter is the time left in the
// window that refused the request, and is zero whenever the request is admitted.
func (l *Limiter) Allow(key string) (allowed bool, retryAfter time.Duration) {
	if !l.enabled.Load() {
		return true, 0
	}
	if key == "" {
		return true, 0 // cannot key the client → fail open
	}
	burst := l.burst.Load()
	period := time.Duration(l.period.Load())
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.windows[key]
	switch {
	case w == nil:
		if len(l.windows) >= maxKeys && !l.sweepExpired(now, period) {
			return true, 0 // table full of live windows → fail open
		}
		w = &window{start: now}
		l.windows[key] = w
	case now.Sub(w.start) >= period:
		w.start, w.count = now, 0 // window elapsed → reset
	}
	w.count++
	if int64(w.count) <= burst {
		return true, 0
	}
	// never advertise a zero-second retry
	left := max(period-now.Sub(w.start), time.Second)
	return false, left
}

// Prune drops windows whose period has elapsed; a daemon calls it periodically to
// keep the table small.
func (l *Limiter) Prune() {
	period := time.Duration(l.period.Load())
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepExpired(now, period)
}

// sweepExpired deletes windows whose period has elapsed and reports whether it
// freed at least one slot. The caller must hold l.mu.
func (l *Limiter) sweepExpired(now time.Time, period time.Duration) bool {
	freed := false
	for k, w := range l.windows {
		if now.Sub(w.start) >= period {
			delete(l.windows, k)
			freed = true
		}
	}
	return freed
}
