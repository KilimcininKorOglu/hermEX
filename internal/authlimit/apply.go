package authlimit

import (
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// Settings is the stored configuration a daemon reads for its login limiter. It is
// the directory's LoginLockoutSettings; the alias keeps callers from importing the
// directory package just to name the type in a closure.
type Settings = directory.LoginLockoutSettings

// Reader reads the stored settings. Every daemon with a login chokepoint passes
// (*directory.SQLDirectory).GetLoginLockoutSettings; a test passes a stub.
type Reader func() (Settings, bool, error)

// applyInterval is how often a daemon re-reads the settings, and pruneInterval how
// often it drops elapsed counters. They match the HTTP and inbound-SMTP limiters'
// cadence, so an admin change takes effect within a minute everywhere.
const (
	applyInterval = time.Minute
	pruneInterval = time.Hour
)

// Apply reads the stored settings and applies them to the limiter. A missing row or
// a read error leaves the limiter as it is: an operator who has saved nothing keeps
// the built-in defaults, and a transient read error keeps the last applied tuning
// rather than snapping back to them. daemon names the caller in the log line, and
// logger carries the failure to the central store: a read that keeps failing leaves
// the lockout tuning stale, which is exactly what an operator must be able to see.
func Apply(daemon string, logger *logging.Logger, l *Limiter, read Reader) {
	s, found, err := read()
	if err != nil {
		logging.SettingsReadFailed(logger, daemon, "login-lockout",
			"leaving the current tuning in place", err)
		return
	}
	if !found {
		return
	}
	l.SetLimits(s.MaxFails, time.Duration(s.WindowSeconds)*time.Second,
		time.Duration(s.LockoutSeconds)*time.Second)
}

// RunMaintenance re-applies the settings every minute so an admin change takes
// effect without a restart, and prunes the tracking table hourly to keep it
// bounded. It runs until the process exits.
func RunMaintenance(daemon string, logger *logging.Logger, l *Limiter, read Reader) {
	applyTick := time.NewTicker(applyInterval)
	pruneTick := time.NewTicker(pruneInterval)
	defer applyTick.Stop()
	defer pruneTick.Stop()
	for {
		select {
		case <-applyTick.C:
			Apply(daemon, logger, l, read)
		case <-pruneTick.C:
			l.Prune()
		}
	}
}
