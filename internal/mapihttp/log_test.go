package mapihttp

import (
	"fmt"
	"net/http/httptest"
	"strings"
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

// TestFailedLoginIsRecordedWithoutThePassword proves a rejected login records
// auth.fail with the user it tried, as IMAP and POP3 do, and that no event
// carries the password the client sent.
func TestFailedLoginIsRecordedWithoutThePassword(t *testing.T) {
	const tried = "Wrong-Secret-Value-42"
	sink := &captureSink{}
	accs := directory.StaticAccounts{"alice@test": {Password: "right", MailboxPath: t.TempDir()}}
	srv := NewServer(accs, accs, "mail.hermex.test", nil)
	srv.SetLogger(logging.New(sink))
	r := httptest.NewRequest("POST", "/mapi/emsmdb/", nil)
	r.SetBasicAuth("alice@test", tried)
	if _, _, ok := srv.basicAuth(httptest.NewRecorder(), r); ok {
		t.Fatal("a wrong password was accepted")
	}
	e, ok := sink.find("auth.fail")
	if !ok {
		t.Fatal("the failed login recorded no auth.fail event")
	}
	if e.User != "alice@test" {
		t.Errorf("auth.fail user = %q, want alice@test", e.User)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, e := range sink.events {
		if strings.Contains(fmt.Sprintf("%+v", e), tried) {
			t.Errorf("event %q carries the password the client sent", e.Name)
		}
	}
}

// TestMAPILogsNspiBind proves the MAPI/HTTP server logs an nspi.bind event tagged
// with the user and client address. An empty body fails the bind, which still
// logs the event (it is recorded regardless of the result), so the test needs no
// hand-built NSPI body.
func TestMAPILogsNspiBind(t *testing.T) {
	sink := &captureSink{}
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: provisionedDir(t)}}
	srv := NewServer(accs, accs, "mail.hermex.test", nil)
	srv.SetLogger(logging.New(sink))
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
