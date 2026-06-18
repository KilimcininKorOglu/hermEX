package objectstore

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"hermex/internal/logging"
)

// captureSink records every event for assertion.
type captureSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (c *captureSink) Write(e logging.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *captureSink) find(name string) (logging.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

func (c *captureSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// TestStoreLogsReadError proves a read served from a broken store surfaces a
// store.error event. This is the coverage the protocol layer cannot give: an IMAP
// FETCH, a POP3 RETR, or a webmail render logs only the request, never the store
// failure underneath it. Closing the store makes the next index query fail with a
// genuine infrastructure error.
func TestStoreLogsReadError(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &captureSink{}
	st.logger = logging.New(sink)
	st.Close()

	if _, err := st.GetMessageRaw(1, 1); err == nil {
		t.Fatal("GetMessageRaw on a closed store: want an error, got nil")
	}
	e, ok := sink.find("error")
	if !ok {
		t.Fatal("no store error event for a read failure")
	}
	if e.Subsystem != logging.Store || e.Fields["op"] != "read" {
		t.Errorf("event = subsystem %q op %v, want store/read", e.Subsystem, e.Fields["op"])
	}
	if !strings.Contains(e.Err, "closed") {
		t.Errorf("err = %q, want a database-closed infrastructure error", e.Err)
	}
}

// TestStoreLogsAppendError proves a failed write surfaces a store.error event.
// The IMAP APPEND, EWS CreateItem, ActiveSync, and webmail draft-save paths reach
// AppendMessage but log no store error of their own, so without this the failure
// is invisible. Closing the store forces a real failure on the next write.
func TestStoreLogsAppendError(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &captureSink{}
	st.logger = logging.New(sink)
	st.Close()

	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: x\r\n\r\nhi\r\n")
	if _, err := st.AppendMessage(1, raw, time.Now(), 0); err == nil {
		t.Fatal("AppendMessage on a closed store: want an error, got nil")
	}
	e, ok := sink.find("error")
	if !ok {
		t.Fatal("no store error event for an append failure")
	}
	if e.Subsystem != logging.Store || e.Fields["op"] != "append" {
		t.Errorf("event = subsystem %q op %v, want store/append", e.Subsystem, e.Fields["op"])
	}
}

// TestStoreReadNotFoundIsNotLogged proves a benign miss is not reported as an
// infrastructure failure: ErrNotFound is a logical outcome, and flooding the
// audit with every not-found would bury the real store errors. The store stays
// open, so only the missing UID — not a broken database — drives the result.
func TestStoreReadNotFoundIsNotLogged(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	sink := &captureSink{}
	st.logger = logging.New(sink)

	if _, err := st.GetMessageRaw(1, 99999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMessageRaw of a missing message: err = %v, want ErrNotFound", err)
	}
	if n := sink.count(); n != 0 {
		t.Errorf("logged %d events for a benign not-found; want 0", n)
	}
}

// TestSetDefaultLoggerStampsStore proves a daemon's SetDefaultLogger reaches a
// store opened afterwards. Production opens never set the logger field directly —
// they rely on this stamp — so the wiring itself must be verified, not just the
// field-level emit.
func TestSetDefaultLoggerStampsStore(t *testing.T) {
	sink := &captureSink{}
	SetDefaultLogger(logging.New(sink))
	defer SetDefaultLogger(nil)

	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()
	if _, err := st.GetMessageRaw(1, 1); err == nil {
		t.Fatal("GetMessageRaw on a closed store: want an error")
	}
	if _, ok := sink.find("error"); !ok {
		t.Fatal("SetDefaultLogger did not reach the store opened after it")
	}
}
