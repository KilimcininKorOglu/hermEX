package mapihttp

import (
	"net/http/httptest"
	"sync"
	"testing"

	"hermex/internal/directory"
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

// TestMAPILogsNspiBind proves the MAPI/HTTP server logs an nspi.bind event tagged
// with the user and client address. An empty body fails the bind, which still
// logs the event (it is recorded regardless of the result), so the test needs no
// hand-built NSPI body.
func TestMAPILogsNspiBind(t *testing.T) {
	sink := &captureSink{}
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: t.TempDir()}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	srv.Logger = logging.New(sink)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp := mapiPost(t, ts, "/mapi/nspi", "Bind", nil, nil)
	resp.Body.Close()

	e, ok := sink.find("bind")
	if !ok {
		t.Fatal("no nspi.bind event")
	}
	if e.Subsystem != logging.NSPI {
		t.Errorf("bind subsystem = %q, want nspi", e.Subsystem)
	}
	if e.User != testUser {
		t.Errorf("bind user = %q, want %s", e.User, testUser)
	}
	if e.RemoteAddr == "" {
		t.Error("nspi.bind event has no client address")
	}
}
