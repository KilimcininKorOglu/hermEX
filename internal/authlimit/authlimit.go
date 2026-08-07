// Package authlimit throttles repeated failed logins to blunt online
// brute-force and credential-stuffing attacks. It is a small in-process,
// per-daemon limiter shared by the login chokepoints (webmail, IMAP, POP3):
// each records failures under a caller-chosen key (client IP for the line
// protocols, account for webmail) and is locked out once too many failures
// accrue inside a rolling window, until a cooldown elapses.
//
// It fails open: an unkeyable caller or a full tracking table admits rather
// than lock a legitimate user out, exactly like the MTA's inbound limiter.
package authlimit

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// IPKey reduces an "ip:port" remote address to its bare IP, the key the line
// protocols (IMAP, POP3) rate-limit on so every connection from one host shares a
// counter; the source port changes on each new connection. An address with no
// port (or an empty one) is returned unchanged.
func IPKey(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// maxKeys bounds memory: distinct keys tracked at once. A full table of still-
// live entries admits rather than evicting a live counter.
const maxKeys = 100_000

// attempt is one key's failure counter and, once tripped, its lockout deadline.
type attempt struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
}

// Limiter tracks failed-login counts per key and locks a key out after too many
// failures within window, for the configured lockout. The zero value is unusable;
// build one with New.
//
// The tuning is held atomically so an operator's change applies to a running
// daemon: a credential-stuffing wave has to be answerable by tightening the
// threshold, and a lockout storm hitting legitimate users by loosening it, without
// rebuilding and restarting the affected daemon.
type Limiter struct {
	mu       sync.Mutex
	attempts map[string]*attempt
	maxFails atomic.Int64
	window   atomic.Int64     // rolling window in nanoseconds
	lockout  atomic.Int64     // cooldown in nanoseconds
	now      func() time.Time // injectable clock for tests
}

// Default tuning: five failures inside a fifteen-minute window locks the key out
// for fifteen minutes. Conservative enough not to trip a fat-fingering human, tight
// enough to make online guessing hopeless.
const (
	DefaultMaxFails = 5
	DefaultWindow   = 15 * time.Minute
	DefaultLockout  = 15 * time.Minute
)

// New builds a limiter with the given tuning, falling back to the defaults for any
// non-positive argument so a caller can pass zeros to mean "standard".
func New(maxFails int, window, lockout time.Duration) *Limiter {
	if maxFails <= 0 {
		maxFails = DefaultMaxFails
	}
	if window <= 0 {
		window = DefaultWindow
	}
	if lockout <= 0 {
		lockout = DefaultLockout
	}
	l := &Limiter{attempts: make(map[string]*attempt), now: time.Now}
	l.maxFails.Store(int64(maxFails))
	l.window.Store(int64(window))
	l.lockout.Store(int64(lockout))
	return l
}

// SetLimits retunes a running limiter. A non-positive value leaves that setting
// alone rather than adopting it: a limiter that locks out after zero failures, or
// counts within a zero-length window, would lock out every login on the daemon.
// Safe to call concurrently with serving.
func (l *Limiter) SetLimits(maxFails int, window, lockout time.Duration) {
	if maxFails > 0 {
		l.maxFails.Store(int64(maxFails))
	}
	if window > 0 {
		l.window.Store(int64(window))
	}
	if lockout > 0 {
		l.lockout.Store(int64(lockout))
	}
}

// MaxFails, Window and Lockout report the current tuning.
func (l *Limiter) MaxFails() int          { return int(l.maxFails.Load()) }
func (l *Limiter) Window() time.Duration  { return time.Duration(l.window.Load()) }
func (l *Limiter) Lockout() time.Duration { return time.Duration(l.lockout.Load()) }

// Allowed reports whether a login attempt for key may proceed. An empty key (the
// caller could not identify the client) always passes, so the limiter never blocks
// what it cannot attribute. A key still inside its lockout window is refused.
func (l *Limiter) Allowed(key string) bool {
	if key == "" {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[key]
	if a == nil {
		return true
	}
	return !now.Before(a.lockedUntil)
}

// Fail records one failed attempt for key and locks the key out once the failures
// reach the threshold inside the rolling window. An empty key is ignored.
func (l *Limiter) Fail(key string) {
	if key == "" {
		return
	}
	now := l.now()
	window, lockout := time.Duration(l.window.Load()), time.Duration(l.lockout.Load())
	maxFails := l.maxFails.Load()
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[key]
	if a == nil {
		if len(l.attempts) >= maxKeys && !l.sweep(now, window) {
			return // table full of live entries → fail open, do not track
		}
		a = &attempt{windowStart: now}
		l.attempts[key] = a
	}
	// A fresh window, or a lockout that has since elapsed, resets the counter so the
	// key gets a clean slate rather than staying locked forever.
	if now.Sub(a.windowStart) >= window || (!a.lockedUntil.IsZero() && now.After(a.lockedUntil)) {
		a.count, a.windowStart, a.lockedUntil = 0, now, time.Time{}
	}
	a.count++
	if int64(a.count) >= maxFails {
		a.lockedUntil = now.Add(lockout)
	}
}

// Succeed clears any failure state for key after a successful login, so a user who
// eventually gets their password right is not held to a stale counter.
func (l *Limiter) Succeed(key string) {
	if key == "" {
		return
	}
	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}

// Prune drops entries whose window has elapsed and are not actively locked out. A
// daemon calls it periodically so the tracking table does not grow to its cap and
// start failing open on hosts that see many distinct client addresses.
func (l *Limiter) Prune() {
	now := l.now()
	window := time.Duration(l.window.Load())
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now, window)
}

// sweep drops entries whose window has elapsed and are not actively locked out,
// reporting whether it freed a slot. The caller must hold l.mu.
func (l *Limiter) sweep(now time.Time, window time.Duration) bool {
	freed := false
	for k, a := range l.attempts {
		if now.Sub(a.windowStart) >= window && now.After(a.lockedUntil) {
			delete(l.attempts, k)
			freed = true
		}
	}
	return freed
}
