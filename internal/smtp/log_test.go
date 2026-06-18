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
// — a connection accept, the envelope sender and recipient, and the accepted
// message — each tagged with the client address (so every log line carries the
// originating IP).
func TestInstrumentationLogsTransaction(t *testing.T) {
	sink := &captureSink{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &Server{Backend: &fakeBackend{sess: &fakeSession{}}, Hostname: "mail.test", Logger: logging.New(sink)}
	go srv.Serve(ln)

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
		fmt.Fprintf(conn, "%s\r\n", line)
		if _, _, err := r.ReadResponse(code); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
	}
	send("EHLO client.test", 250)
	send("MAIL FROM:<alice@example.com>", 250)
	send("RCPT TO:<bob@hermex.test>", 250)
	fmt.Fprint(conn, "DATA\r\n")
	if _, _, err := r.ReadResponse(354); err != nil {
		t.Fatalf("DATA: %v", err)
	}
	fmt.Fprint(conn, "Subject: hi\r\n\r\nbody\r\n.\r\n")
	if _, _, err := r.ReadResponse(250); err != nil {
		t.Fatalf("end of DATA: %v", err)
	}

	events := sink.snapshot()

	mustRemote := func(e logging.Event) {
		if e.RemoteAddr == "" {
			t.Errorf("%s event has no client address", e.Name)
		}
	}
	if e, ok := findEvent(events, "conn.accept"); !ok {
		t.Error("no conn.accept event")
	} else {
		mustRemote(e)
	}
	if e, ok := findEvent(events, "mail.from"); !ok {
		t.Error("no mail.from event")
	} else {
		if e.Fields["from"] != "alice@example.com" {
			t.Errorf("mail.from from = %v, want alice@example.com", e.Fields["from"])
		}
		mustRemote(e)
	}
	if e, ok := findEvent(events, "rcpt.to"); !ok {
		t.Error("no rcpt.to event")
	} else {
		if e.Fields["to"] != "bob@hermex.test" {
			t.Errorf("rcpt.to to = %v, want bob@hermex.test", e.Fields["to"])
		}
		mustRemote(e)
	}
	if e, ok := findEvent(events, "message.accept"); !ok {
		t.Error("no message.accept event")
	} else {
		mustRemote(e)
	}
}
