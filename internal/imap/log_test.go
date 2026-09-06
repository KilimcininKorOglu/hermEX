package imap

import (
	"bufio"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
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

func (c *captureSink) snapshot() []logging.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]logging.Event(nil), c.events...)
}

// find returns the first captured event with the given name.
func find(events []logging.Event, name string) (logging.Event, bool) {
	for _, e := range events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

// TestInstrumentationLogsConnAndAuth proves the IMAP server emits the audit events
// , a connection accept, a successful auth tagged with the user, and a failed auth
// tagged with the attempted login, and that no password reaches the log.
func TestInstrumentationLogsConnAndAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	mustNoErr(t, "open the mailbox", err)
	st.Close()

	sink := &captureSink{}
	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, "listen", err)
	defer ln.Close()
	go func() { _ = (&Server{Auth: auth, Hostname: "mail.test", Logger: logging.New(sink)}).Serve(ln) }()

	// The failed login goes first: it leaves the connection unauthenticated, so the
	// successful one still runs. (A second LOGIN after success would be rejected as
	// "already authenticated" before reaching the auth check.)
	loginTranscript(t, ln.Addr().String(), "a LOGIN bob hunter2", "b LOGIN alice secret")

	events := sink.snapshot()
	if _, ok := find(events, "conn.accept"); !ok {
		t.Error("no conn.accept event")
	}
	wantAuthEvent(t, events, "auth.ok", "alice", logging.LevelInfo)
	wantAuthEvent(t, events, "auth.fail", "bob", logging.LevelWarn)

	// No password may appear anywhere in any rendered event.
	var rendered strings.Builder
	rs := logging.NewStderrSink(&rendered)
	for _, e := range events {
		rs.Write(e)
	}
	wantNotContains(t, "the rendered events", rendered.String(), "secret")
	wantNotContains(t, "the rendered events", rendered.String(), "hunter2")
}

// loginTranscript dials the server, consumes the greeting, and sends each line,
// reading its response. The auth event is emitted before the tagged response, so
// reading the response guarantees the event has been captured.
func loginTranscript(t *testing.T, addr string, lines ...string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	mustNoErr(t, "dial", err)
	defer conn.Close()
	br := bufio.NewReader(conn)
	_, err = br.ReadString('\n') // greeting
	mustNoErr(t, "read the greeting", err)
	for _, line := range lines {
		_, err := conn.Write([]byte(line + "\r\n"))
		mustNoErr(t, "write "+line, err)
		_, err = br.ReadString('\n')
		mustNoErr(t, "read the response to "+line, err)
	}
}

// wantAuthEvent checks one authentication event's user and level.
func wantAuthEvent(t *testing.T, events []logging.Event, name, user string, level logging.Level) {
	t.Helper()
	e, ok := find(events, name)
	if !ok {
		t.Errorf("no %s event", name)
		return
	}
	wantEq(t, name+" user", e.User, user)
	wantEq(t, name+" level", e.Level, level)
}
