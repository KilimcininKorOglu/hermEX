package httplimit

import (
	"log"
	"time"

	"hermex/internal/directory"
)

// Settings is the stored configuration a daemon reads for its limiter. It is the
// directory's HTTPRateLimitSettings; the alias keeps callers from importing the
// directory package just to name the type in a closure.
type Settings = directory.HTTPRateLimitSettings

// Reader reads the stored settings. Every HTTP daemon passes
// (*directory.SQLDirectory).GetHTTPRateLimitSettings; a test passes a stub.
type Reader func() (Settings, bool, error)

// applyInterval is how often a daemon re-reads the settings, and pruneInterval how
// often it drops elapsed windows. They match the inbound-SMTP limiter's cadence, so
// an admin change takes effect within a minute everywhere.
const (
	applyInterval = time.Minute
	pruneInterval = time.Hour
)

// Apply reads the stored settings and applies them to the limiter. A missing row
// or a read error leaves the limiter as it is, so a settings failure never starts
// throttling unexpectedly and a transient read error keeps the last applied value
// rather than flipping the limiter off. daemon names the caller in the log line.
func Apply(daemon string, l *Limiter, read Reader) {
	s, found, err := read()
	if err != nil {
		log.Printf("%s: HTTP rate-limit settings read failed, leaving rate limiting unchanged: %v", daemon, err)
		return
	}
	if !found {
		return
	}
	l.SetLimits(s.Burst, time.Duration(s.WindowSeconds)*time.Second)
	l.SetEnabled(s.Enabled)
}

// RunMaintenance re-applies the settings every minute so an admin change takes
// effect without a restart, and prunes the limiter's window table hourly to keep
// it bounded. It runs until the process exits.
func RunMaintenance(daemon string, l *Limiter, read Reader) {
	applyTick := time.NewTicker(applyInterval)
	pruneTick := time.NewTicker(pruneInterval)
	defer applyTick.Stop()
	defer pruneTick.Stop()
	for {
		select {
		case <-applyTick.C:
			Apply(daemon, l, read)
		case <-pruneTick.C:
			l.Prune()
		}
	}
}
