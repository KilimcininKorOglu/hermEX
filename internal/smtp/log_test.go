package smtp

import (
	"bufio"
	"fmt"
	"net"
	"net/textproto"
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

// TestInstrumentationLogsTransaction proves the SMTP server logs a message intake
// , a connection accept, the envelope sender and recipient, and the accepted
// message, each tagged with the client address (so every log line carries the
// originating IP).
func TestInstrumentationLogsTransaction(t *testing.T) {
	sink := &captureSink{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &Server{Backend: &fakeBackend{sess: &fakeSession{}}, Hostname: "mail.test", Logger: logging.New(sink)}
	go func() { _ = srv.Serve(ln) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := textproto.NewReader(bufio.NewReader(conn))
	if _, _, err := r.ReadResponse(220); err != nil { // greeting
		t.Fatalf("greeting: %v", err)
	}

	send := func(line string, code int) {
		_, _ = fmt.Fprintf(conn, "%s\r\n", line)
		if _, _, err := r.ReadResponse(code); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
	}
	send("EHLO client.test", 250)
	send("MAIL FROM:<alice@example.com>", 250)
	send("RCPT TO:<bob@hermex.test>", 250)
	_, _ = fmt.Fprint(conn, "DATA\r\n")
	if _, _, err := r.ReadResponse(354); err != nil {
		t.Fatalf("DATA: %v", err)
	}
	_, _ = fmt.Fprint(conn, "Subject: hi\r\n\r\nbody\r\n.\r\n")
	if _, _, err := r.ReadResponse(250); err != nil {
		t.Fatalf("end of DATA: %v", err)
	}

	events := sink.snapshot()

	wantLoggedEvent(t, events, "conn.accept", "", nil)
	wantLoggedEvent(t, events, "mail.from", "from", "alice@example.com")
	wantLoggedEvent(t, events, "rcpt.to", "to", "bob@hermex.test")
	wantLoggedEvent(t, events, "message.accept", "", nil)
}

// wantLoggedEvent asserts one event was logged, that it carries the client
// address (so every line traces back to the originating IP), and, when a field is
// named, that the field carries the expected value.
func wantLoggedEvent(t *testing.T, events []logging.Event, name, field string, want any) {
	t.Helper()
	e, ok := findEvent(events, name)
	if !ok {
		t.Errorf("no %s event", name)
		return
	}
	if e.RemoteAddr == "" {
		t.Errorf("%s event has no client address", name)
	}
	if field != "" && e.Fields[field] != want {
		t.Errorf("%s %s = %v, want %v", name, field, e.Fields[field], want)
	}
}
