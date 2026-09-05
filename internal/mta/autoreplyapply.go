package mta

import (
	"sync/atomic"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// AutoReplySettings is the stored configuration a daemon reads for the
// out-of-office reply. The alias keeps callers from importing the directory
// package just to name the type in a closure.
type AutoReplySettings = directory.AutoReplySettings

// AutoReplyReader reads the stored settings. Every daemon passes
// (*directory.SQLDirectory).GetAutoReplySettings; a test passes a stub.
type AutoReplyReader func() (AutoReplySettings, bool, error)

// autoReplyApplyInterval is how often a daemon re-reads the settings. It matches
// the other settings families' cadence, so an admin change takes effect within a
// minute.
const autoReplyApplyInterval = time.Minute

// DefaultAutoReplyPrefix is the prefix used when no row has been saved. The
// value lives in the directory package, because the admin form that edits the
// setting has to show the same default the MTA falls back to.
const DefaultAutoReplyPrefix = directory.DefaultAutoReplySubjectPrefix

// autoReplyPrefix is what composeAutoReplySubject reads. It is a package value
// rather than a parameter threaded through delivery because the out-of-office
// pass is reached from the delivery path of every daemon, and each of them polls
// the same row.
var autoReplyPrefix atomic.Value // string

// SetAutoReplyPrefix installs the prefix the out-of-office reply uses when the
// mailbox stores no subject. An empty string restores the built-in default.
func SetAutoReplyPrefix(prefix string) {
	if prefix == "" {
		prefix = DefaultAutoReplyPrefix
	}
	autoReplyPrefix.Store(prefix)
}

// currentAutoReplyPrefix returns the installed prefix, or the built-in default
// when no daemon has installed one (which is what a test binary sees).
func currentAutoReplyPrefix() string {
	if v, ok := autoReplyPrefix.Load().(string); ok && v != "" {
		return v
	}
	return DefaultAutoReplyPrefix
}

// ApplyAutoReplySettings reads the stored settings and installs the prefix. A
// missing row installs the built-in default; a read error leaves the running
// prefix alone, so a transient failure does not silently change the wording of
// every reply the deployment sends.
func ApplyAutoReplySettings(daemon string, logger *logging.Logger, read AutoReplyReader) {
	s, found, err := read()
	if err != nil {
		logging.SettingsReadFailed(logger, daemon, "autoreply",
			"leaving the auto-reply subject prefix unchanged", err)
		return
	}
	if !found {
		SetAutoReplyPrefix(DefaultAutoReplyPrefix)
		return
	}
	SetAutoReplyPrefix(s.SubjectPrefix)
}

// RunAutoReplyMaintenance re-applies the settings every minute so an admin
// change takes effect without a restart. It runs until the process exits.
func RunAutoReplyMaintenance(daemon string, logger *logging.Logger, read AutoReplyReader) {
	tick := time.NewTicker(autoReplyApplyInterval)
	defer tick.Stop()
	for range tick.C {
		ApplyAutoReplySettings(daemon, logger, read)
	}
}

// StartAutoReply applies the stored settings and starts the maintenance loop.
// Only the daemon that delivers mail calls it, because the out-of-office pass
// runs in delivery and nowhere else.
func StartAutoReply(daemon string, logger *logging.Logger, read AutoReplyReader) {
	ApplyAutoReplySettings(daemon, logger, read)
	go RunAutoReplyMaintenance(daemon, logger, read)
}
