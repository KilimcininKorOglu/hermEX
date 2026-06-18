package activesync

import (
	"net/http/httptest"
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

// TestActiveSyncLogsCommand proves dispatch logs an activesync command event with
// the command name, the user, and the real client address (the X-Forwarded-For
// hop behind the gateway). An unimplemented command keeps the test off the
// store-backed handlers; the event is emitted before the command switch.
func TestActiveSyncLogsCommand(t *testing.T) {
	sink := &captureSink{}
	srv := &Server{Logger: logging.New(sink)}
	r := httptest.NewRequest("POST", "/Microsoft-Server-ActiveSync", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	srv.dispatch(httptest.NewRecorder(), r, &session{user: "alice@test", req: asRequest{cmd: "Settings"}})

	e, ok := sink.find("command")
	if !ok {
		t.Fatal("no activesync command event")
	}
	if e.Subsystem != logging.ActiveSync || e.Fields["cmd"] != "Settings" {
		t.Errorf("command event = subsystem %q cmd %v, want activesync/Settings", e.Subsystem, e.Fields["cmd"])
	}
	if e.User != "alice@test" {
		t.Errorf("user = %q, want alice@test", e.User)
	}
	if e.RemoteAddr != "203.0.113.9" {
		t.Errorf("remote = %q, want the X-Forwarded-For client 203.0.113.9", e.RemoteAddr)
	}
}
