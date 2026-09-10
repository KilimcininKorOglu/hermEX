package connlimit

import (
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// Settings is the stored configuration a daemon reads for its limiter. It is the
// directory's ConnLimitSettings; the alias keeps callers from importing the
// directory package just to name the type in a closure.
type Settings = directory.ConnLimitSettings

// Reader reads the stored settings. Every connection-oriented daemon passes
// (*directory.SQLDirectory).GetConnLimitSettings; a test passes a stub.
type Reader func() (Settings, bool, error)

// applyInterval is how often a daemon re-reads the settings. It matches the other
// limiters' cadence, so an admin change takes effect within a minute everywhere.
const applyInterval = time.Minute

// Apply reads the stored settings and applies them to the limiter. A missing row
// or a read error leaves the limiter as it is, so a settings failure never starts
// refusing connections unexpectedly and a transient read error keeps the last
// applied value rather than flipping the cap off. daemon names the caller in the
// log line, and logger carries the failure to the central store, so an operator
// can see that the running caps have stopped tracking the stored ones.
func Apply(daemon string, logger *logging.Logger, l *Limiter, read Reader) {
	s, found, err := read()
	if err != nil {
		logging.SettingsReadFailed(logger, daemon, "connection-limit",
			"leaving the connection caps unchanged", err)
		return
	}
	if !found {
		return
	}
	l.SetLimits(s.MaxTotal, s.MaxPerClient)
	l.SetEnabled(s.Enabled)
}

// RunMaintenance re-applies the settings every minute so an admin change takes
// effect without a restart. It runs until the process exits.
func RunMaintenance(daemon string, logger *logging.Logger, l *Limiter, read Reader) {
	tick := time.NewTicker(applyInterval)
	defer tick.Stop()
	for range tick.C {
		Apply(daemon, logger, l, read)
	}
}
