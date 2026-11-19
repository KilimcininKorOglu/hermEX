package logging

import (
	"context"
	"time"
)

// spillLimiter is a sink whose spill file can be capped (MongoSink).
type spillLimiter interface{ SetSpillLimit(limit int64) }

// SetSpillLimit caps the spill file of every sink behind the logger at limit
// bytes; zero removes the cap. A logger with no spilling sink ignores it.
func (l *Logger) SetSpillLimit(limit int64) {
	if l == nil {
		return
	}
	setSpillLimit(l.sink, limit)
}

func setSpillLimit(s Sink, limit int64) {
	switch v := s.(type) {
	case *MultiSink:
		for _, m := range v.sinks {
			setSpillLimit(m, limit)
		}
	case spillLimiter:
		v.SetSpillLimit(limit)
	}
}

// SpillLimitReader reads the operator's per-daemon spill cap in MiB (0: no cap).
// Every directory-backed daemon passes (*directory.SQLDirectory).GetLogSpillLimitMB.
type SpillLimitReader func() (int, error)

// spillLimitInterval is how often a daemon re-reads the cap, matching the other
// settings families, so a change on the admin panel applies within a minute.
const spillLimitInterval = time.Minute

// ApplySpillLimit reads the cap and applies it to the logger. A read failure
// keeps the cap already applied and is recorded, so an operator can see that the
// running cap has stopped tracking the setting.
func ApplySpillLimit(daemon string, l *Logger, read SpillLimitReader) {
	mb, err := read()
	if err != nil {
		SettingsReadFailed(l, daemon, "log-spill-limit", "leaving the spill cap unchanged", err)
		return
	}
	l.SetSpillLimit(int64(max(mb, 0)) << 20)
}

// RunSpillLimit applies the cap at once and then every minute until ctx ends.
func RunSpillLimit(ctx context.Context, daemon string, l *Logger, read SpillLimitReader) {
	ApplySpillLimit(daemon, l, read)
	tick := time.NewTicker(spillLimitInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			ApplySpillLimit(daemon, l, read)
		}
	}
}
