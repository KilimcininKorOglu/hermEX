package ews

import (
	"encoding/base64"
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
	srv := NewServer(accs, accs, "mail.hermex.test")
	srv.Logger = logging.New(sink)
	r := httptest.NewRequest("POST", "/EWS/Exchange.asmx", nil)
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
	// Basic auth carries the password base64-encoded (RFC 7617), so a logged
	// Authorization header leaks it without the plain text ever appearing.
	encoded := base64.StdEncoding.EncodeToString([]byte("alice@test:" + tried))
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, e := range sink.events {
		if dump := fmt.Sprintf("%+v", e); strings.Contains(dump, tried) || strings.Contains(dump, encoded) {
			t.Errorf("event %q carries the password the client sent", e.Name)
		}
	}
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
	// realUser is the authenticated principal serveEWS always sets; the operation
	// log records it (so impersonation cannot hide who made the request).
	srv.dispatch(httptest.NewRecorder(), r, &session{user: "alice@test", realUser: "alice@test"})

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
