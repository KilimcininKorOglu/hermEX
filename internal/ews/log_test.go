package ews

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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

// TestEWSLogsOperation proves dispatch logs an EWS operation event with the SOAP
// operation name, the user, and the real client address. An unsupported operation
// keeps the test off the store-backed handlers; the event is emitted before the
// operation switch.
func TestEWSLogsOperation(t *testing.T) {
	sink := &captureSink{}
	srv := &Server{Logger: logging.New(sink)}
	r := httptest.NewRequest("POST", "/EWS/Exchange.asmx", strings.NewReader(wrapRequest("<FooBar/>")))
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	srv.dispatch(httptest.NewRecorder(), r, &session{user: "alice@test"})

	e, ok := sink.find("operation")
	if !ok {
		t.Fatal("no ews operation event")
	}
	if e.Subsystem != logging.EWS || e.Fields["op"] != "FooBar" {
		t.Errorf("operation event = subsystem %q op %v, want ews/FooBar", e.Subsystem, e.Fields["op"])
	}
	if e.User != "alice@test" {
		t.Errorf("user = %q, want alice@test", e.User)
	}
	if e.RemoteAddr != "203.0.113.9" {
		t.Errorf("remote = %q, want the X-Forwarded-For client 203.0.113.9", e.RemoteAddr)
	}
}

// TestEWSLogsICSSync proves a folder-hierarchy sync emits an ics sync event under
// the ics subsystem (EWS sync is ICS-backed). The handler logs before parsing, so
// a malformed body keeps the test off the store while still emitting the event.
func TestEWSLogsICSSync(t *testing.T) {
	sink := &captureSink{}
	srv := &Server{Logger: logging.New(sink)}
	srv.handleSyncFolderHierarchy(httptest.NewRecorder(), []byte("not xml"), &session{user: "alice@test"})

	e, ok := sink.find("sync")
	if !ok {
		t.Fatal("no ics sync event")
	}
	if e.Subsystem != logging.ICS || e.Fields["scope"] != "folder-hierarchy" {
		t.Errorf("sync event = subsystem %q scope %v, want ics/folder-hierarchy", e.Subsystem, e.Fields["scope"])
	}
	if e.User != "alice@test" {
		t.Errorf("user = %q, want alice@test", e.User)
	}
}
