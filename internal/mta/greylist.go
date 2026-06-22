package mta

import (
	"net"
	"strings"
	"sync/atomic"
	"time"

	"hermex/internal/directory"
)

// Greylist policy defaults. A first contact is deferred; a retry is accepted only
// after the minimum delay, and only while the triplet has not expired. Confirmed
// triplets are remembered for the confirmed TTL; unconfirmed deferrals expire sooner.
// These seed a new Greylister's timings; an operator's edit replaces them at runtime
// via SetTimings, with no restart.
const (
	defaultGreylistMinDelay       = 5 * time.Minute
	defaultGreylistUnconfirmedTTL = 24 * time.Hour
	defaultGreylistConfirmedTTL   = 36 * 24 * time.Hour
)

// GreylistStore is the triplet persistence the greylister needs.
// *directory.SQLDirectory satisfies it.
type GreylistStore interface {
	GreylistGet(ipKey, sender, recipient string) (directory.Greylisted, bool, error)
	GreylistUpsertSeen(ipKey, sender, recipient string, now int64) error
	GreylistConfirm(ipKey, sender, recipient string, now int64) error
	PruneGreylist(unconfirmedBefore, confirmedBefore int64) error
}

// allowChecker reports whether a sender is allowlisted (and so exempt from
// greylisting). *antispam.Scorer satisfies it.
type allowChecker interface {
	Allowlisted(mailFrom, fromDomain string) bool
}

// Greylister defers a first-contact triplet so a legitimate MTA retries; a spammer
// that does not retry never gets through. It is disabled by default (an admin
// enables it). Authenticated submission, allowlisted senders, and bounces are
// exempt, and any store error fails open (accept) — greylisting never loses mail on
// its own failure.
type Greylister struct {
	store   GreylistStore
	allow   allowChecker
	enabled atomic.Bool
	// minDelay, unconfirmedTTL, and confirmedTTL are the greylist timings in seconds,
	// held atomically so the MTA's poll can apply an operator's edit while delivery
	// runs, with no restart. NewGreylister seeds them with the built-in defaults.
	minDelay       atomic.Int64
	unconfirmedTTL atomic.Int64
	confirmedTTL   atomic.Int64
	now            func() int64 // injectable clock for tests
}

// NewGreylister builds a greylister over a triplet store and an allowlist checker
// (either may drive exemptions); it starts disabled, with the built-in default
// timings.
func NewGreylister(store GreylistStore, allow allowChecker) *Greylister {
	g := &Greylister{store: store, allow: allow, now: func() int64 { return time.Now().Unix() }}
	g.minDelay.Store(int64(defaultGreylistMinDelay.Seconds()))
	g.unconfirmedTTL.Store(int64(defaultGreylistUnconfirmedTTL.Seconds()))
	g.confirmedTTL.Store(int64(defaultGreylistConfirmedTTL.Seconds()))
	return g
}

// SetEnabled turns greylisting on or off; safe to call concurrently with delivery.
func (g *Greylister) SetEnabled(on bool) { g.enabled.Store(on) }

// SetTimings installs the greylist timings, each in seconds: the minimum delay before
// a first-seen triplet is accepted, and the TTLs for unconfirmed and confirmed
// triplets. It is safe to call concurrently with delivery, so an operator's edit
// applies without a restart. A non-positive value for any timing leaves that one
// unchanged, so a misconfiguration cannot remove the delay or collapse a TTL.
func (g *Greylister) SetTimings(minDelay, unconfirmedTTL, confirmedTTL int64) {
	if minDelay > 0 {
		g.minDelay.Store(minDelay)
	}
	if unconfirmedTTL > 0 {
		g.unconfirmedTTL.Store(unconfirmedTTL)
	}
	if confirmedTTL > 0 {
		g.confirmedTTL.Store(confirmedTTL)
	}
}

// ShouldDefer reports whether to defer this recipient with a temporary failure so
// the sender retries. It returns false (accept) whenever greylisting is off, the
// sender is exempt (a bounce or an allowlisted sender), the client IP cannot be
// keyed, or any store operation errors.
func (g *Greylister) ShouldDefer(ip net.IP, sender, recipient string) bool {
	if !g.enabled.Load() || sender == "" {
		return false
	}
	if g.allow != nil && g.allow.Allowlisted(sender, "") {
		return false
	}
	ipKey := networkKey(ip)
	if ipKey == "" {
		return false // cannot key the sender → fail open
	}
	sender, recipient = strings.ToLower(sender), strings.ToLower(recipient)
	now := g.now()
	rec, found, err := g.store.GreylistGet(ipKey, sender, recipient)
	if err != nil {
		return false // fail open
	}
	switch {
	case !found:
		if err := g.store.GreylistUpsertSeen(ipKey, sender, recipient, now); err != nil {
			return false // could not record → accept rather than defer forever
		}
		return true // first contact
	case rec.Confirmed:
		_ = g.store.GreylistUpsertSeen(ipKey, sender, recipient, now) // refresh TTL, best-effort
		return false
	case now-rec.FirstSeen >= g.minDelay.Load():
		if err := g.store.GreylistConfirm(ipKey, sender, recipient, now); err != nil {
			return false
		}
		return false // retried after the delay → accept and confirm
	default:
		return true // retried too soon
	}
}

// Prune removes expired triplets; the MTA calls it periodically to bound the table.
func (g *Greylister) Prune() error {
	now := g.now()
	return g.store.PruneGreylist(now-g.unconfirmedTTL.Load(), now-g.confirmedTTL.Load())
}

// networkKey masks a client IP to its network — a /24 for IPv4, /64 for IPv6 — so a
// provider that retries from a different IP in the same pool keys to the same
// triplet. It returns "" for a nil IP so the caller fails open.
func networkKey(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		mask := net.CIDRMask(24, 32)
		return (&net.IPNet{IP: v4.Mask(mask), Mask: mask}).String()
	}
	mask := net.CIDRMask(64, 128)
	return (&net.IPNet{IP: ip.Mask(mask), Mask: mask}).String()
}
