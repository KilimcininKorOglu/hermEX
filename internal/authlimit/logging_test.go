package authlimit

import (
	"errors"
	"testing"

	"hermex/internal/logging"
)

// captureSink collects the events an Apply pass emits.
type captureSink struct{ events []logging.Event }

func (s *captureSink) Write(e logging.Event) { s.events = append(s.events, e) }

// TestApplyRecordsAReadFailure proves a settings-read failure reaches the central
// log rather than only stderr. The failure is deliberately swallowed (the running
// tuning stays in place so logins keep working), which is exactly why it has to be
// recorded: otherwise a lasting directory outage silently freezes the lockout
// policy an operator thinks is live.
func TestApplyRecordsAReadFailure(t *testing.T) {
	sink := &captureSink{}
	l := New(0, 0, 0)

	Apply("hermex-test", logging.New(sink), l, func() (Settings, bool, error) {
		return Settings{}, false, errors.New("directory unreachable")
	})

	if len(sink.events) != 1 {
		t.Fatalf("read failure emitted %d events, want 1", len(sink.events))
	}
	e := sink.events[0]
	if e.Level != logging.LevelWarn || e.Subsystem != logging.System || e.Name != "settings.read.fail" {
		t.Errorf("event = %s/%s/%s, want warn/system/settings.read.fail", e.Level, e.Subsystem, e.Name)
	}
	if e.Fields["daemon"] != "hermex-test" || e.Fields["settings"] != "login-lockout" {
		t.Errorf("fields = %v, want the daemon and the settings family named", e.Fields)
	}
	if got, _ := e.Fields["error"].(string); got != "directory unreachable" {
		t.Errorf("error field = %q, want the read error", got)
	}
}

// TestApplyStaysSilentOnACleanRead is the negative control: the poll runs once a
// minute in every daemon, so a pass that logs when nothing is wrong would bury the
// log store in noise.
func TestApplyStaysSilentOnACleanRead(t *testing.T) {
	sink := &captureSink{}
	l := New(0, 0, 0)
	logger := logging.New(sink)

	Apply("hermex-test", logger, l, func() (Settings, bool, error) {
		return Settings{MaxFails: 3, WindowSeconds: 60, LockoutSeconds: 60}, true, nil
	})
	Apply("hermex-test", logger, l, func() (Settings, bool, error) {
		return Settings{}, false, nil // no stored row
	})

	if len(sink.events) != 0 {
		t.Errorf("clean reads emitted %d events, want none: %+v", len(sink.events), sink.events)
	}
}
