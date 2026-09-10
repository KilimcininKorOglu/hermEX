// Package connlimit caps how many connections a connection-oriented daemon
// serves at once, in total and per client address. It is the concurrency
// counterpart to the per-client request limiter in internal/httplimit: the same
// atomic tunables, the same disabled-by-default posture, and the same
// admin-tunable-without-restart contract, applied to the accept loop in
// internal/lifecycle instead of to an HTTP handler.
package connlimit

import (
	"sync"
	"sync/atomic"
)

// Defaults. They are generous on purpose: a mail client opens several
// connections per account (a desktop IMAP client commonly holds one per open
// folder), and one client address is often a whole office behind a NAT. Like the
// other two limiters, this one starts DISABLED so an operator opts in.
const (
	defaultMaxTotal     = 1000
	defaultMaxPerClient = 20
)

// Limiter counts the connections a daemon is serving and reports when a new one
// would pass a cap. It is in-process (per daemon), safe for concurrent use,
// disabled until an operator enables it, and admits whenever it is off.
type Limiter struct {
	mu      sync.Mutex
	total   int
	perAddr map[string]int // live connections per client address

	maxTotal     atomic.Int64
	maxPerClient atomic.Int64
	enabled      atomic.Bool
}

// New builds a limiter with the default caps; it starts disabled.
func New() *Limiter {
	l := &Limiter{perAddr: make(map[string]int)}
	l.maxTotal.Store(defaultMaxTotal)
	l.maxPerClient.Store(defaultMaxPerClient)
	return l
}

// SetEnabled turns the cap on or off; safe to call concurrently with serving, so
// an operator's toggle applies without a restart.
func (l *Limiter) SetEnabled(on bool) { l.enabled.Store(on) }

// SetLimits sets the total and per-client caps. A value below 1 is ignored,
// leaving the current setting, so the limiter is never configured to admit no
// connection at all.
func (l *Limiter) SetLimits(total, perClient int) {
	if total >= 1 {
		l.maxTotal.Store(int64(total))
	}
	if perClient >= 1 {
		l.maxPerClient.Store(int64(perClient))
	}
}

// Enabled reports whether the cap is currently on.
func (l *Limiter) Enabled() bool { return l.enabled.Load() }

// MaxTotal reports the current total cap.
func (l *Limiter) MaxTotal() int { return int(l.maxTotal.Load()) }

// MaxPerClient reports the current per-client cap.
func (l *Limiter) MaxPerClient() int { return int(l.maxPerClient.Load()) }

// Reason names the cap that refused a connection, for the log line an operator
// reads to tell "the daemon is full" from "this one client is".
type Reason string

const (
	// ReasonTotal is the daemon's total cap.
	ReasonTotal Reason = "total"
	// ReasonPerClient is one client address's cap.
	ReasonPerClient Reason = "per-client"
)

// Acquire takes a connection slot for a client address. It returns the release
// to call when the connection ends, and ok=false with the cap that refused it.
// The limiter admits everything while it is off, and an empty key counts against
// the total only, because a connection whose address cannot be read must still
// be bounded by something. release is idempotent, so a caller that releases
// twice cannot drive the counters below zero.
func (l *Limiter) Acquire(key string) (release func(), ok bool, why Reason) {
	if !l.enabled.Load() {
		return func() {}, true, ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if int64(l.total) >= l.maxTotal.Load() {
		return nil, false, ReasonTotal
	}
	if key != "" && int64(l.perAddr[key]) >= l.maxPerClient.Load() {
		return nil, false, ReasonPerClient
	}
	l.total++
	if key != "" {
		l.perAddr[key]++
	}
	var once sync.Once
	return func() { once.Do(func() { l.release(key) }) }, true, ""
}

// release gives one slot back.
func (l *Limiter) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total > 0 {
		l.total--
	}
	if key == "" {
		return
	}
	// The map holds live connections only, so dropping the last one drops the key
	// and the table stays bounded by the total cap.
	if n := l.perAddr[key] - 1; n > 0 {
		l.perAddr[key] = n
	} else {
		delete(l.perAddr, key)
	}
}

// InUse reports how many connections are held right now, in total and for one
// client address.
func (l *Limiter) InUse(key string) (total, perClient int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total, l.perAddr[key]
}
