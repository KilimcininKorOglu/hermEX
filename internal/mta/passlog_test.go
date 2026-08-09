package mta

import (
	"errors"
	"sync"
	"testing"

	"hermex/internal/logging"
	"hermex/internal/objectstore"
)

// passSink records the events a delivery pass emits.
type passSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (s *passSink) Write(e logging.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *passSink) find(name string) (logging.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

// withPassLogger installs a capturing logger for one test and restores whatever
// was there afterwards.
func withPassLogger(t *testing.T) *passSink {
	t.Helper()
	sink := &passSink{}
	prev := defaultLogger.Load()
	defaultLogger.Store(logging.New(sink))
	t.Cleanup(func() { defaultLogger.Store(prev) })
	return sink
}

// TestDeliveryPassFailureIsRecorded proves a post-delivery pass that could not
// run reaches the central log. Each pass is wrapped in a recover so a bug in an
// optional step cannot lose the message, which is right, but it also means the
// failure is otherwise invisible: the mail arrives and nothing says the rule, the
// auto-reply or the meeting update never ran. Only stderr carried it, and the
// operator's log viewer reads the central store.
func TestDeliveryPassFailureIsRecorded(t *testing.T) {
	sink := withPassLogger(t)

	logPassFailure("inbox-rules", "", logging.Fields{"uid": uint32(42)}, "boom")

	e, ok := sink.find("delivery.pass.fail")
	if !ok {
		t.Fatal("a failed delivery pass produced no event")
	}
	if e.Level != logging.LevelError {
		t.Errorf("level = %q, want error", e.Level)
	}
	if e.Subsystem != logging.MTA {
		t.Errorf("subsystem = %q, want mta", e.Subsystem)
	}
	if e.Fields["pass"] != "inbox-rules" {
		t.Errorf("pass = %v, want the pass that failed", e.Fields["pass"])
	}
	if e.Fields["uid"] != uint32(42) {
		t.Errorf("uid = %v, want the message it failed on", e.Fields["uid"])
	}
	if e.Err != "boom" {
		t.Errorf("err = %q, want the cause", e.Err)
	}
}

// TestDeliveryPassFailureNamesTheMailbox proves the out-of-office pass reports
// which account it failed for. Without it an operator sees that some reply did
// not go out but not whose.
func TestDeliveryPassFailureNamesTheMailbox(t *testing.T) {
	sink := withPassLogger(t)

	logPassFailure("out-of-office", "alice@hermex.test", nil, "store unavailable")

	e, ok := sink.find("delivery.pass.fail")
	if !ok {
		t.Fatal("no event")
	}
	if e.User != "alice@hermex.test" {
		t.Errorf("user = %q, want the mailbox the pass ran for", e.User)
	}
	if e.Fields["pass"] != "out-of-office" {
		t.Errorf("pass = %v, want out-of-office", e.Fields["pass"])
	}
}

// TestDeliveryPassLoggingIsOptional proves a daemon that installed no logger
// still delivers. Logging must never be able to fail the mail path.
func TestDeliveryPassLoggingIsOptional(t *testing.T) {
	prev := defaultLogger.Load()
	defaultLogger.Store(nil)
	t.Cleanup(func() { defaultLogger.Store(prev) })

	logPassFailure("inbox-rules", "", logging.Fields{"uid": uint32(1)}, "boom")
}

// TestMeetingReplyTrackingFailureIsRecorded proves a failed tracking update on an
// inbound meeting REPLY reaches the central log. The reply is delivered as a
// normal message either way, so if the write of the attendee's response status
// fails, the only visible effect is an organizer whose Tracking tab shows that
// attendee as never having answered. Discarding the error left nothing to look at.
func TestMeetingReplyTrackingFailureIsRecorded(t *testing.T) {
	sink := withPassLogger(t)

	prev := OnMeetingReply
	OnMeetingReply = func(*objectstore.Store, string, int64) (bool, error) {
		return true, errors.New("store write failed")
	}
	t.Cleanup(func() { OnMeetingReply = prev })

	autoProcessReply(nil, "bob@hermex.test", objectstore.MessageInfo{UID: 7})

	e, ok := sink.find("delivery.pass.fail")
	if !ok {
		t.Fatal("a failed meeting-reply tracking update produced no event")
	}
	if e.Fields["pass"] != "meeting-reply" {
		t.Errorf("pass = %v, want meeting-reply", e.Fields["pass"])
	}
	if e.Fields["uid"] != uint32(7) {
		t.Errorf("uid = %v, want the message it failed on", e.Fields["uid"])
	}
	if e.Err != "store write failed" {
		t.Errorf("err = %q, want the store's error", e.Err)
	}
}

// TestMeetingReplySuccessIsSilent is the negative control: the common case runs on
// every delivered reply, so logging it would be noise, not signal.
func TestMeetingReplySuccessIsSilent(t *testing.T) {
	sink := withPassLogger(t)

	prev := OnMeetingReply
	OnMeetingReply = func(*objectstore.Store, string, int64) (bool, error) { return true, nil }
	t.Cleanup(func() { OnMeetingReply = prev })

	autoProcessReply(nil, "bob@hermex.test", objectstore.MessageInfo{UID: 7})

	if _, ok := sink.find("delivery.pass.fail"); ok {
		t.Error("a successful tracking update emitted a failure event")
	}
}
