package webmail2api

import (
	"errors"
	"sync"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// errSink collects everything the package logs.
type errSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (s *errSink) Write(e logging.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

// find returns the first event recorded for an op, if any.
func (s *errSink) find(op string) (logging.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.Fields["op"] == op {
			return e, true
		}
	}
	return logging.Event{}, false
}

// withSink installs a collecting logger for one test and restores the previous
// one afterwards, so a package-level logger never leaks between tests.
func withSink(t *testing.T) *errSink {
	t.Helper()
	prev := defaultLogger.Load()
	sink := &errSink{}
	SetDefaultLogger(logging.New(sink))
	t.Cleanup(func() { defaultLogger.Store(prev) })
	return sink
}

// TestBestEffortFailureIsRecorded proves a swallowed failure still reaches the
// operator. The request path continues on purpose (the mail is already
// delivered), so this line is the only difference between "Sent copy filed" and
// "Sent copy lost".
func TestBestEffortFailureIsRecorded(t *testing.T) {
	sink := withSink(t)

	logError("file-sent-copy", errors.New("disk full"), logging.Fields{"user": "alice@hermex.test"})

	e, ok := sink.find("file-sent-copy")
	if !ok {
		t.Fatal("the failure was not recorded anywhere")
	}
	if e.Level != logging.LevelError {
		t.Errorf("level = %v, want error", e.Level)
	}
	if e.Subsystem != logging.Webmail {
		t.Errorf("subsystem = %v, want webmail", e.Subsystem)
	}
	if e.Err != "disk full" {
		t.Errorf("Err = %q, want the underlying error", e.Err)
	}
	if e.Fields["user"] != "alice@hermex.test" {
		t.Errorf("the event does not name the account: %v", e.Fields)
	}
}

// TestLogErrorWithoutALoggerIsSafe holds the library baseline: the package is
// used in tests and by callers that install no logger, and logging must never be
// what breaks a request path.
func TestLogErrorWithoutALoggerIsSafe(t *testing.T) {
	prev := defaultLogger.Load()
	SetDefaultLogger(nil)
	t.Cleanup(func() { defaultLogger.Store(prev) })

	logError("file-sent-copy", errors.New("boom"), nil)
	logError("file-sent-copy", nil, nil)
}

// panicPushStore is a pushStore whose subscriber enumeration panics, to drive
// safePoll's recover path.
type panicPushStore struct{}

func (panicPushStore) SavePushSubscription(directory.PushSubscription) error { return nil }
func (panicPushStore) ListPushSubscriptions(string) ([]directory.PushSubscription, error) {
	return nil, nil
}
func (panicPushStore) DeletePushSubscription(string) error { return nil }
func (panicPushStore) PushSubscriberEmails() ([]string, error) {
	panic("push poll blew up")
}

// TestSafePollLogsRecoveredPanic proves a panicking poll no longer vanishes: the
// recover records the panic value so an operator can find why web push stopped,
// instead of the panic being discarded and leaving a clean log with no push.
func TestSafePollLogsRecoveredPanic(t *testing.T) {
	sink := withSink(t)

	s := &Server{}
	s.safePoll(panicPushStore{}, map[string]int{}) // must not crash the process

	e, ok := sink.find("push-poll-panic")
	if !ok {
		t.Fatal("the recovered push-poll panic was not recorded")
	}
	if e.Level != logging.LevelError {
		t.Errorf("level = %v, want error", e.Level)
	}
	if e.Err == "" {
		t.Error("the panic value was discarded; the operator has no lead to the cause")
	}
}
