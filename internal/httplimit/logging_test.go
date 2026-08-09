package httplimit

import (
	"errors"
	"testing"

	"hermex/internal/logging"
)

// captureSink collects the events an Apply pass emits.
type captureSink struct{ events []logging.Event }

func (s *captureSink) Write(e logging.Event) { s.events = append(s.events, e) }

// TestApplyRecordsAReadFailure proves the request-rate limiter's settings-read
// failure reaches the central log. The limiter keeps its current state on a failed
// read, so without this record an operator has no way to tell that the running
// limits stopped tracking the stored ones.
func TestApplyRecordsAReadFailure(t *testing.T) {
	sink := &captureSink{}
	l := NewLimiter()

	Apply("hermex-test", logging.New(sink), l, func() (Settings, bool, error) {
		return Settings{}, false, errors.New("directory unreachable")
	})

	if len(sink.events) != 1 {
		t.Fatalf("read failure emitted %d events, want 1", len(sink.events))
	}
	e := sink.events[0]
	if e.Level != logging.LevelWarn || e.Name != "settings.read.fail" {
		t.Errorf("event = %s/%s, want warn/settings.read.fail", e.Level, e.Name)
	}
	if e.Fields["settings"] != "http-rate-limit" {
		t.Errorf("settings field = %v, want http-rate-limit", e.Fields["settings"])
	}
}

// TestApplyStaysSilentOnACleanRead is the negative control: a per-minute poll that
// logs on success would drown the store.
func TestApplyStaysSilentOnACleanRead(t *testing.T) {
	sink := &captureSink{}
	l := NewLimiter()

	Apply("hermex-test", logging.New(sink), l, func() (Settings, bool, error) {
		return Settings{Enabled: true, Burst: 100, WindowSeconds: 60}, true, nil
	})

	if len(sink.events) != 0 {
		t.Errorf("a clean read emitted %d events, want none: %+v", len(sink.events), sink.events)
	}
}
