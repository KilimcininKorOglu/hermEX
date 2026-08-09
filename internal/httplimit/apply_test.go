package httplimit

import (
	"errors"
	"testing"
	"time"
)

// TestApplyInjectsStoredSettings proves a stored row reaches the limiter: the toggle,
// the burst and the window all take effect.
func TestApplyInjectsStoredSettings(t *testing.T) {
	l := NewLimiter()
	read := func() (Settings, bool, error) {
		return Settings{Enabled: true, Burst: 42, WindowSeconds: 15}, true, nil
	}
	Apply("test", nil, l, read)
	if !l.Enabled() {
		t.Error("limiter disabled after applying an enabled row")
	}
	if l.Burst() != 42 {
		t.Errorf("burst = %d, want 42", l.Burst())
	}
	if l.Period() != 15*time.Second {
		t.Errorf("period = %v, want 15s", l.Period())
	}
}

// TestApplyReinjectsOnEveryPoll proves the poll path adopts a later change: an operator
// who edits the settings sees them take effect without a daemon restart, and a later
// disable turns the limiter back off.
func TestApplyReinjectsOnEveryPoll(t *testing.T) {
	l := NewLimiter()
	stored := Settings{Enabled: true, Burst: 100, WindowSeconds: 60}
	read := func() (Settings, bool, error) { return stored, true, nil }

	Apply("test", nil, l, read)
	if l.Burst() != 100 {
		t.Fatalf("burst after first poll = %d, want 100", l.Burst())
	}

	stored = Settings{Enabled: true, Burst: 250, WindowSeconds: 30}
	Apply("test", nil, l, read) // the ticker calls exactly this
	if l.Burst() != 250 || l.Period() != 30*time.Second {
		t.Errorf("after the second poll = %d/%v, want 250/30s", l.Burst(), l.Period())
	}

	stored.Enabled = false
	Apply("test", nil, l, read)
	if l.Enabled() {
		t.Error("limiter still enabled after the operator turned it off")
	}
}

// TestApplyKeepsSettingsOnReadError proves a transient read failure leaves the limiter
// exactly as it was, rather than flipping it off (which would drop the protection) or
// on (which would throttle clients because of a database blip).
func TestApplyKeepsSettingsOnReadError(t *testing.T) {
	l := NewLimiter()
	Apply("test", nil, l, func() (Settings, bool, error) {
		return Settings{Enabled: true, Burst: 77, WindowSeconds: 20}, true, nil
	})
	Apply("test", nil, l, func() (Settings, bool, error) {
		return Settings{}, false, errors.New("directory unreachable")
	})
	if !l.Enabled() || l.Burst() != 77 || l.Period() != 20*time.Second {
		t.Errorf("after a read error = enabled %v %d/%v, want the last applied 77/20s",
			l.Enabled(), l.Burst(), l.Period())
	}
}

// TestApplyKeepsDefaultsWhenUnset proves an installation that never saved the settings
// keeps the limiter's built-in defaults, disabled, so upgrading does not start
// throttling anyone.
func TestApplyKeepsDefaultsWhenUnset(t *testing.T) {
	l := NewLimiter()
	Apply("test", nil, l, func() (Settings, bool, error) { return Settings{}, false, nil })
	if l.Enabled() {
		t.Error("limiter enabled with no stored row, want disabled")
	}
	if l.Burst() != defaultBurst || l.Period() != defaultWindow {
		t.Errorf("limits = %d/%v, want the defaults %d/%v", l.Burst(), l.Period(), defaultBurst, defaultWindow)
	}
}
