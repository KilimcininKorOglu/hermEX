package main

import (
	"strings"
	"sync"
	"testing"

	"hermex/internal/logging"
)

// digestSink records what a digest pass reported.
type digestSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (s *digestSink) Write(e logging.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *digestSink) find(name string) (logging.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

// TestDigestWithNoSecretIsReportedNotSilent covers the half of this failure the
// operator can actually act on. Every digest entry carries a signed release link,
// so with no signing secret a pass can produce nothing at all. The worker used to
// not start in that case, which meant the panel showed the digest enabled, no
// summaries went out, and nothing anywhere said why. Quarantined legitimate mail
// then sits unnoticed for exactly as long as nobody notices the summaries stopped.
func TestDigestWithNoSecretIsReportedNotSilent(t *testing.T) {
	sink := &digestSink{}
	if digestCanSend(nil, logging.New(sink)) {
		t.Fatal("a digest with no signing secret claims it can send")
	}

	e, ok := sink.find("digest.disabled")
	if !ok {
		t.Fatal("the digest silently produced nothing, so the operator never learns of it")
	}
	if e.Level != logging.LevelWarn || e.Subsystem != logging.MTA {
		t.Errorf("event = %s/%s, want warn/mta", e.Level, e.Subsystem)
	}
	// The message has to name the missing setting, or the operator has the symptom
	// and no idea which knob produces it.
	if !strings.Contains(e.Err, "digest_secret") {
		t.Errorf("the report does not name the missing setting: %q", e.Err)
	}
}

// TestDigestWithASecretSaysNothing guards the other direction. A working digest
// that warns on every hourly check trains the operator to ignore the warning.
func TestDigestWithASecretSaysNothing(t *testing.T) {
	sink := &digestSink{}
	if !digestCanSend([]byte("signing-secret"), logging.New(sink)) {
		t.Fatal("a configured digest is refused")
	}
	if _, ok := sink.find("digest.disabled"); ok {
		t.Error("a working digest reports itself as unable to send")
	}
}

// TestDigestReportsEveryPass proves the report is not one-shot. The worker checks
// hourly; a secret can be added at any point, and until it is, each pass that
// produces nothing has to leave a trace of its own.
func TestDigestReportsEveryPass(t *testing.T) {
	sink := &digestSink{}
	logger := logging.New(sink)
	for range 3 {
		digestCanSend(nil, logger)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 3 {
		t.Errorf("3 passes produced %d reports, want one each", len(sink.events))
	}
}
