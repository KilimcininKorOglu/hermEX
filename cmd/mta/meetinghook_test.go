package main

import (
	"errors"
	"sync"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
)

// hookSink records every event the hook reports.
type hookSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (s *hookSink) Write(e logging.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

// TestMeetingHookRecordsFailure is the swallowed-failure defect. The pass runs
// after delivery has already succeeded, so its error is discarded on purpose; it
// used to go to stderr instead of the central sink, which is where the sibling
// post-delivery passes report and the only place an operator looks. Without it a
// failure leaves the organizer never told the request was accepted or declined and
// nothing explaining why.
func TestMeetingHookRecordsFailure(t *testing.T) {
	sink := &hookSink{}
	failing := func(*objectstore.Store, directory.Accounts, *relay.Spool, string, int64) (bool, error) {
		return false, errors.New("directory unavailable")
	}

	hook := meetingHook(failing, logging.New(sink))
	if handled := hook(nil, nil, "room@hermex.test", 42); handled {
		t.Errorf("a failed pass reported the request as handled")
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, e := range sink.events {
		if e.Name == "meeting.autoprocess.fail" {
			if e.User != "room@hermex.test" {
				t.Errorf("event user = %q, want the recipient", e.User)
			}
			if e.Err == "" {
				t.Error("the event carries no error text, so the cause is still unknown")
			}
			return
		}
	}
	t.Errorf("the auto-process failure never reached the central sink; events = %+v", sink.events)
}

// TestMeetingHookStaysQuietOnSuccess is the control: an ordinary pass must not
// report anything.
func TestMeetingHookStaysQuietOnSuccess(t *testing.T) {
	sink := &hookSink{}
	ok := func(*objectstore.Store, directory.Accounts, *relay.Spool, string, int64) (bool, error) {
		return true, nil
	}

	hook := meetingHook(ok, logging.New(sink))
	if handled := hook(nil, nil, "room@hermex.test", 42); !handled {
		t.Errorf("a successful pass did not report the request as handled")
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 0 {
		t.Errorf("a successful pass logged %+v, want nothing", sink.events)
	}
}
