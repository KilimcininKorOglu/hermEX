package activesync

import (
	"encoding/base64"
	"fmt"
	"net/http"
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

// TestFailedLoginNeverLogsThePassword proves a rejected login records no event
// carrying the password the client sent: credentials are never logged, a failed
// attempt included, since the log store is readable by operators who must not
// learn a user's password (or a near miss of it).
func TestFailedLoginNeverLogsThePassword(t *testing.T) {
	const tried = "Wrong-Secret-Value-42"
	sink := &captureSink{}
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: t.TempDir()}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	srv.Logger = logging.New(sink)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest("POST", ts.URL+"/Microsoft-Server-ActiveSync?Cmd=FolderSync&User="+testUser+"&DeviceId=dev1&DeviceType=iPhone", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser, tried)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want 401", resp.StatusCode)
	}

	// The attempt is recorded, as IMAP and POP3 record theirs, with the user it
	// tried and never the password.
	e, ok := sink.find("auth.fail")
	if !ok {
		t.Fatal("the failed login recorded no auth.fail event")
	}
	wantEq(t, "auth.fail user", e.User, testUser)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	// Basic auth carries the password base64-encoded (RFC 7617), so a logged
	// Authorization header leaks it without the plain text ever appearing.
	encoded := base64.StdEncoding.EncodeToString([]byte(testUser + ":" + tried))
	for _, e := range sink.events {
		dump := fmt.Sprintf("%+v", e)
		if strings.Contains(dump, tried) || strings.Contains(dump, encoded) {
			t.Errorf("event %q carries the password the client sent", e.Name)
		}
	}
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
