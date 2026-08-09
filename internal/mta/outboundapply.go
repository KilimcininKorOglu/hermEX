package mta

import (
	"errors"
	"sync/atomic"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// OutboundSettings is the stored configuration a daemon reads for its outbound
// limiter. The alias keeps callers from importing the directory package just to
// name the type in a closure.
type OutboundSettings = directory.OutboundSettings

// OutboundReader reads the stored settings. Every daemon passes
// (*directory.SQLDirectory).GetOutboundSettings; a test passes a stub.
type OutboundReader func() (OutboundSettings, bool, error)

// outboundApplyInterval is how often a daemon re-reads the settings, and
// outboundPruneInterval how often it drops elapsed windows. They match the other
// limiters' cadence, so an admin change takes effect within a minute everywhere.
const (
	outboundApplyInterval = time.Minute
	outboundPruneInterval = time.Hour
)

// ErrOutboundLimited is returned by DeliverAndRelay when the sender has already
// reached its external-recipient cap for the current window. It is deliberately
// NOT terminal: the send is deferred, so a scheduled send retries once the window
// rolls rather than being dropped.
var ErrOutboundLimited = errors.New("too many external recipients in a short time")

// outboundLimiter is the limiter DeliverAndRelay consults. A daemon installs one
// through StartOutboundLimiter; with none installed the behaviour is unchanged.
var outboundLimiter atomic.Pointer[OutboundLimiter]

// SetOutboundLimiter installs the limiter every send path is measured against.
// Passing nil removes it (what a test does on cleanup).
func SetOutboundLimiter(l *OutboundLimiter) { outboundLimiter.Store(l) }

// ApplyOutboundSettings reads the stored settings and applies them to the limiter.
// A missing row or a read error leaves the limiter as it is, so a settings failure
// never starts throttling unexpectedly and a transient read error keeps the last
// applied value rather than flipping the limiter off. daemon names the caller in
// the log line, and logger carries the failure to the central store so an operator
// can see that the running cap has stopped tracking the stored one.
func ApplyOutboundSettings(daemon string, logger *logging.Logger, l *OutboundLimiter, read OutboundReader) {
	s, found, err := read()
	if err != nil {
		logging.SettingsReadFailed(logger, daemon, "outbound",
			"leaving outbound limiting unchanged", err)
		return
	}
	if !found {
		return
	}
	l.SetLimits(s.RecipientCap, time.Duration(s.WindowSeconds)*time.Second)
	l.SetEnabled(s.Enabled)
}

// RunOutboundMaintenance re-applies the settings every minute so an admin change
// takes effect without a restart, and prunes the limiter's window table hourly to
// keep it bounded. It runs until the process exits.
func RunOutboundMaintenance(daemon string, logger *logging.Logger, l *OutboundLimiter, read OutboundReader) {
	applyTick := time.NewTicker(outboundApplyInterval)
	pruneTick := time.NewTicker(outboundPruneInterval)
	defer applyTick.Stop()
	defer pruneTick.Stop()
	for {
		select {
		case <-applyTick.C:
			ApplyOutboundSettings(daemon, logger, l, read)
		case <-pruneTick.C:
			l.Prune()
		}
	}
}

// StartOutboundLimiter builds the limiter for one daemon, reports an over-cap
// account to the central log, applies the stored settings, starts the maintenance
// loop and installs the limiter for DeliverAndRelay. Every binary that can queue
// external mail calls it, so a compromised account is capped whichever protocol it
// sends through, not only SMTP submission. The returned limiter is also what the
// SMTP backend and the inbox-rule hooks hold, so one account has one counter per
// process.
func StartOutboundLimiter(daemon string, logger *logging.Logger, read OutboundReader) *OutboundLimiter {
	l := NewOutboundLimiter()
	l.SetAlerter(func(user string, count int) {
		logger.Emit(logging.Event{
			Level:     logging.LevelError,
			Subsystem: logging.MTA,
			Name:      "outbound.abuse",
			User:      user,
			Fields:    logging.Fields{"recipients": count},
		})
	})
	ApplyOutboundSettings(daemon, logger, l, read)
	go RunOutboundMaintenance(daemon, logger, l, read)
	SetOutboundLimiter(l)
	return l
}

// limitOutbound counts the external recipients of one send against the sender's
// window and reports whether the send must be deferred. A recipient whose domain
// cannot be classified is not counted: the routing error surfaces on the ordinary
// path below, and the limiter fails open rather than blocking on a lookup failure.
func limitOutbound(accounts directory.Accounts, from string, recipients []string) error {
	l := outboundLimiter.Load()
	if l == nil {
		return nil
	}
	for _, rcpt := range recipients {
		external, err := isExternalDomain(accounts, rcpt)
		if err != nil || !external {
			continue
		}
		if !l.Allow(from) {
			return ErrOutboundLimited
		}
	}
	return nil
}
