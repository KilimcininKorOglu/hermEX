// Package authlimit throttles repeated failed logins to blunt online
// brute-force and credential-stuffing attacks. It is a small in-process,
// per-daemon limiter shared by every login chokepoint (webmail, IMAP, POP3,
// SMTP submission, EWS, ActiveSync, DAV, MAPI/HTTP, the admin panel).
//
// Every attempt is counted on two axes at once, the client address and the
// account, and refused when either is locked out. One axis alone defends one
// shape of attack: an address counter never sees guessing against a single
// mailbox distributed over a botnet, and an account counter never sees one
// password sprayed across the whole directory from one host.
//
// It fails open for a caller it cannot key, rather than lock a legitimate user
// out. A full tracking table evicts the key idle longest instead of admitting
// untracked, so filling the table cannot switch the limiter off.
package authlimit

import (
	"container/list"
	"hermex/internal/clientnet"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Key namespaces. The two axes share one table, so an account that reads like an
// address must never land on an address's counter.
const (
	ipPrefix   = "ip:"
	acctPrefix = "acct:"
)

// ipFailFactor scales the address axis against the operator's threshold. One
// address is many people (an office behind a NAT, a mobile carrier's gateway),
// so it takes proportionally more failures to lock out than one account does;
// locking a whole site out of webmail over five typos is worse than the attack.
const ipFailFactor = 4

// ipKey reduces a remote address to the address-axis key of the client it names
// (clientnet.Key: the port dropped, an IPv6 address cut to its /64).
func ipKey(remoteAddr string) string {
	if k := clientnet.Key(remoteAddr); k != "" {
		return ipPrefix + k
	}
	return ""
}

// accountKey reduces a login name to its counter key, so a login differing only
// in case or padding shares one.
func accountKey(user string) string {
	user = strings.ToLower(strings.TrimSpace(user))
	if user == "" {
		return ""
	}
	return acctPrefix + user
}

// maxKeys bounds memory: distinct keys tracked at once. A full table of still-
// live entries evicts the key that failed least recently, as nginx limit_req
// does with a full zone: a table that stopped tracking would let anyone who
// filled it guess at any account without limit.
const maxKeys = 100_000

// attempt is one key's failure counter, once tripped its lockout deadline, and
// its place in the recency list. On the address axis byAccount splits the count by
// the account each failure named, so a success forgives only its own account's
// failures on the address.
type attempt struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
	elem        *list.Element // Value is the key
	byAccount   map[string]int
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
	recency  *list.List // keys, most recently failed at the front
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
	l := &Limiter{attempts: make(map[string]*attempt), recency: list.New(), now: time.Now}
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

// Allowed reports whether a login attempt from addr for account may proceed. Both
// axes are checked and either one locked out refuses the attempt. An empty axis
// (the caller could not identify that side) is skipped, so the limiter never
// blocks what it cannot attribute.
func (l *Limiter) Allowed(addr, account string) bool {
	return l.allowedKey(ipKey(addr)) && l.allowedKey(accountKey(account))
}

// allowedKey reports whether one axis is outside its lockout window.
func (l *Limiter) allowedKey(key string) bool {
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

// Fail records one failed attempt against both axes, locking either one out once
// its own threshold is reached inside the rolling window. An empty axis is
// ignored.
func (l *Limiter) Fail(addr, account string) {
	maxFails := l.maxFails.Load()
	l.failKey(ipKey(addr), maxFails*ipFailFactor, accountKey(account))
	l.failKey(accountKey(account), maxFails, "")
}

// failKey records one failed attempt for one axis, locking it out at maxFails. On
// the address axis owner names the account the failure was for ("" when none).
func (l *Limiter) failKey(key string, maxFails int64, owner string) {
	if key == "" {
		return
	}
	now := l.now()
	window, lockout := time.Duration(l.window.Load()), time.Duration(l.lockout.Load())
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[key]
	if a == nil {
		a = l.track(key, now, window)
	}
	l.recency.MoveToFront(a.elem)
	// A fresh window, or a lockout that has since elapsed, resets the counter so the
	// key gets a clean slate rather than staying locked forever.
	if now.Sub(a.windowStart) >= window || (!a.lockedUntil.IsZero() && now.After(a.lockedUntil)) {
		a.count, a.windowStart, a.lockedUntil, a.byAccount = 0, now, time.Time{}, nil
	}
	a.count++
	if owner != "" {
		if a.byAccount == nil {
			a.byAccount = make(map[string]int)
		}
		a.byAccount[owner]++
	}
	if int64(a.count) >= maxFails {
		a.lockedUntil = now.Add(lockout)
	}
}

// Succeed clears the account's own counter and removes from the address's counter
// the failures that named this account, so a user who mistyped and then signed in
// leaves nothing behind. It does not touch failures against other accounts: an
// attacker signing in to its own account between guesses must not reset the
// address axis it is spraying from.
func (l *Limiter) Succeed(addr, account string) {
	acct := accountKey(account)
	l.mu.Lock()
	defer l.mu.Unlock()
	if a := l.attempts[acct]; a != nil {
		l.remove(a.elem)
	}
	if a := l.attempts[ipKey(addr)]; a != nil && acct != "" {
		a.count -= a.byAccount[acct]
		delete(a.byAccount, acct)
		if a.count <= 0 {
			l.remove(a.elem)
		}
	}
}

// track starts counting a new key. A full table first drops the entries whose
// window has elapsed and are not locked out and, when none has, the key that
// failed least recently. The caller must hold l.mu.
func (l *Limiter) track(key string, now time.Time, window time.Duration) *attempt {
	if len(l.attempts) >= maxKeys && !l.sweep(now, window) {
		l.remove(l.recency.Back())
	}
	a := &attempt{windowStart: now, elem: l.recency.PushFront(key)}
	l.attempts[key] = a
	return a
}

// remove drops one tracked key. The caller must hold l.mu.
func (l *Limiter) remove(e *list.Element) {
	delete(l.attempts, e.Value.(string))
	l.recency.Remove(e)
}

// Prune drops entries whose window has elapsed and are not actively locked out. A
// daemon calls it periodically so the tracking table does not grow to its cap on
// hosts that see many distinct client addresses, where a full table evicts live
// counters by recency alone.
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
	for _, a := range l.attempts {
		if now.Sub(a.windowStart) >= window && now.After(a.lockedUntil) {
			l.remove(a.elem)
			freed = true
		}
	}
	return freed
}
