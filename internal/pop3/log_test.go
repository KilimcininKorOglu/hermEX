package pop3

import (
	"bufio"
	"fmt"
	"net"
	"net/textproto"
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

func findEvent(events []logging.Event, name string) (logging.Event, bool) {
	for _, e := range events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

// TestInstrumentationLogsConnAndAuth proves the POP3 server logs a connection
// accept, a failed auth tagged with the attempted login, and a successful auth
// tagged with the user — and that no password reaches the log.
func TestInstrumentationLogsConnAndAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	sink := &captureSink{}
	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go (&Server{Auth: auth, Hostname: "mail.test", Logger: logging.New(sink)}).Serve(ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := textproto.NewReader(bufio.NewReader(conn))
	if _, err := r.ReadLine(); err != nil { // greeting
		t.Fatalf("greeting: %v", err)
	}

	// The auth event is emitted before the response, so reading it guarantees the
	// event was captured. A failed PASS resets the auth state, so the successful
	// login still runs on the same connection.
	send := func(s string) {
		fmt.Fprintf(conn, "%s\r\n", s)
		if _, err := r.ReadLine(); err != nil {
			t.Fatalf("read response to %q: %v", s, err)
		}
	}
	send("USER bob")
	send("PASS hunter2")
	send("USER alice")
	send("PASS secret")

	events := sink.snapshot()
	if _, ok := findEvent(events, "conn.accept"); !ok {
		t.Error("no conn.accept event")
	}
	if e, ok := findEvent(events, "auth.ok"); !ok {
		t.Error("no auth.ok event for the successful login")
	} else if e.User != "alice" || e.Level != logging.LevelInfo {
		t.Errorf("auth.ok = user %q level %v, want alice/info", e.User, e.Level)
	}
	if e, ok := findEvent(events, "auth.fail"); !ok {
		t.Error("no auth.fail event for the wrong password")
	} else if e.User != "bob" || e.Level != logging.LevelWarn {
		t.Errorf("auth.fail = user %q level %v, want bob/warn", e.User, e.Level)
	}

	// No password may appear anywhere in any rendered event.
	var rendered strings.Builder
	rs := logging.NewStderrSink(&rendered)
	for _, e := range events {
		rs.Write(e)
	}
	if out := rendered.String(); strings.Contains(out, "secret") || strings.Contains(out, "hunter2") {
		t.Error("a password leaked into the logged events")
	}
}
