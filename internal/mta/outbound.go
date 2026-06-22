package mta

import (
	"sync"
	"sync/atomic"
	"time"
)

// Outbound abuse defaults: a window and the cap on external recipients one local
// (authenticated) account may send to within it. A compromised account blasting spam
// trips the cap; the excess is deferred and the admin is alerted once per window.
// Chosen high enough to tolerate ordinary use (including a legitimate post to an
// external-heavy list) and DISABLED by default — an admin opts in and tunes.
const (
	defaultOutboundWindow = time.Hour
	defaultOutboundCap    = 500
)

// outboundMaxKeys bounds the limiter's memory: the number of accounts tracked at
// once. A full table of still-live windows fails open (admits) rather than evict a
// live counter — it must never block a legitimate account to reclaim memory.
const outboundMaxKeys = 100_000

// obWindow is one account's fixed-window recipient counter, plus whether the
// over-cap alert has already fired for this window.
type obWindow struct {
	start   time.Time
	count   int
	alerted bool
}

// OutboundLimiter caps how many external recipients a local account may send to in a
// fixed window, deferring the excess so a compromised account cannot blast unchecked,
// and alerting the admin once per window. It is in-process (per MTA), keyed by the
// authenticated account, fails open on any inability to track, and is disabled by
// default.
type OutboundLimiter struct {
	mu      sync.Mutex
	windows map[string]*obWindow
	cap     atomic.Int64 // max external recipients per window (admin-tunable)
	window  atomic.Int64 // window length in nanoseconds (admin-tunable)
	enabled atomic.Bool
	now     func() time.Time             // injectable clock for tests
	onAlert func(user string, count int) // fired once per account per window on the first over-cap recipient
}

// NewOutboundLimiter builds a limiter with the default cap and window; it starts
// disabled and with no alerter.
func NewOutboundLimiter() *OutboundLimiter {
	l := &OutboundLimiter{windows: make(map[string]*obWindow), now: time.Now}
	l.cap.Store(defaultOutboundCap)
	l.window.Store(int64(defaultOutboundWindow))
	return l
}

// SetEnabled turns outbound limiting on or off; safe to call concurrently with
// delivery.
func (l *OutboundLimiter) SetEnabled(on bool) { l.enabled.Store(on) }

// SetLimits sets the recipient cap and the window length. A cap below 1 or a
// non-positive window is ignored, leaving the current setting.
func (l *OutboundLimiter) SetLimits(cap int, window time.Duration) {
	if cap >= 1 {
		l.cap.Store(int64(cap))
	}
	if window > 0 {
		l.window.Store(int64(window))
	}
}

// SetAlerter installs the callback fired once per account per window when an account
// first crosses the cap, so the MTA can record an admin alert.
func (l *OutboundLimiter) SetAlerter(fn func(user string, count int)) { l.onAlert = fn }

// Allow records one external recipient for the account and reports whether it is
// within the cap. It admits (returns true) whenever the limiter is off or the account
// is empty. The first recipient past the cap fires the alert (once per window) and is
// deferred along with every later recipient until the window rolls.
func (l *OutboundLimiter) Allow(user string) bool {
	if !l.enabled.Load() || user == "" {
		return true
	}
	cap := l.cap.Load()
	window := time.Duration(l.window.Load())
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.windows[user]
	switch {
	case w == nil:
		if len(l.windows) >= outboundMaxKeys && !l.sweepExpired(now, window) {
			return true // table full of live windows → fail open
		}
		w = &obWindow{start: now}
		l.windows[user] = w
	case now.Sub(w.start) >= window:
		w.start, w.count, w.alerted = now, 0, false // window elapsed → reset
	}
	w.count++
	if int64(w.count) <= cap {
		return true
	}
	if !w.alerted {
		w.alerted = true
		if l.onAlert != nil {
			l.onAlert(user, w.count)
		}
	}
	return false
}

// Prune drops windows whose period has elapsed; the MTA calls it periodically to keep
// the table small.
func (l *OutboundLimiter) Prune() {
	window := time.Duration(l.window.Load())
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepExpired(now, window)
}

// sweepExpired deletes windows whose period has elapsed and reports whether it freed
// at least one slot. The caller must hold l.mu.
func (l *OutboundLimiter) sweepExpired(now time.Time, window time.Duration) bool {
	freed := false
	for k, w := range l.windows {
		if now.Sub(w.start) >= window {
			delete(l.windows, k)
			freed = true
		}
	}
	return freed
}
