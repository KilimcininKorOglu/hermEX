package smtp

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"

	"hermex/internal/config"
	"hermex/internal/logging"
	"hermex/internal/tlstest"
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

func hasEvent(events []logging.Event, name string) bool {
	for _, e := range events {
		if e.Name == name {
			return true
		}
	}
	return false
}

// TestInstrumentationLogsTransaction proves the SMTP server logs a message intake,
// a connection accept, the envelope sender and recipient, and the accepted
// message, each tagged with the client address.
func TestInstrumentationLogsTransaction(t *testing.T) {
	sink := &captureSink{}
	srv := &Server{Backend: &fakeBackend{sess: &fakeSession{}}, Hostname: "mail.test", Logger: logging.New(sink)}
	ln := serveOn(t, srv)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	r := textproto.NewReader(bufio.NewReader(conn))
	expect(t, r, 220)

	exchange := func(line string, code int) {
		send(t, conn, line+"\r\n")
		expect(t, r, code)
	}
	exchange("EHLO client.test", 250)
	exchange("MAIL FROM:<alice@example.com>", 250)
	exchange("RCPT TO:<bob@hermex.test>", 250)
	exchange("DATA", 354)
	send(t, conn, "Subject: hi\r\n\r\nbody\r\n.\r\n")
	expect(t, r, 250)

	events := sink.snapshot()
	wantLoggedEvent(t, events, "conn.accept", "", nil)
	wantLoggedEvent(t, events, "mail.from", "from", "alice@example.com")
	wantLoggedEvent(t, events, "rcpt.to", "to", "bob@hermex.test")
	wantLoggedEvent(t, events, "message.accept", "", nil)
}

// TestInstrumentationLogsRejectionAndNeverSecrets verifies rejection and
// backend-unavailable events are logged while AUTH credentials never leak into logs.
func TestInstrumentationLogsRejectionAndNeverSecrets(t *testing.T) {
	t.Run("message reject", func(t *testing.T) {
		sink := &captureSink{}
		sess := &fakeSession{dataErr: &PermError{Message: "5.7.1 blocked"}}
		srv := &Server{Backend: &fakeBackend{sess: sess}, Hostname: "mail.test", Logger: logging.New(sink)}
		ln := serveOn(t, srv)
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer mustClose(t, conn)
		r := textproto.NewReader(bufio.NewReader(conn))
		expect(t, r, 220)
		send(t, conn, "EHLO client.test\r\n")
		expect(t, r, 250)
		send(t, conn, "MAIL FROM:<alice@test>\r\n")
		expect(t, r, 250)
		send(t, conn, "RCPT TO:<bob@test>\r\n")
		expect(t, r, 250)
		send(t, conn, "DATA\r\n")
		expect(t, r, 354)
		send(t, conn, "Subject: hi\r\n\r\nbody\r\n.\r\n")
		expect(t, r, 554)

		events := sink.snapshot()
		wantLoggedEvent(t, events, "message.reject", "", nil)
		if hasEvent(events, "message.accept") {
			t.Error("unexpected message.accept event on rejected message")
		}
	})

	t.Run("conn reject", func(t *testing.T) {
		sink := &captureSink{}
		srv := &Server{Backend: failingBackend{errors.New("db down")}, Hostname: "mail.test", Logger: logging.New(sink)}
		ln := serveOn(t, srv)
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer mustClose(t, conn)
		expect(t, textproto.NewReader(bufio.NewReader(conn)), 421)

		events := sink.snapshot()
		wantLoggedEvent(t, events, "conn.reject", "reason", "db down")
		if hasEvent(events, "conn.accept") {
			t.Error("unexpected conn.accept on rejected session")
		}
	})

	t.Run("auth secret never logged", func(t *testing.T) {
		const secret = "s3cr3t-pass-xyz"
		sink := &captureSink{}
		sess := &authSession{user: "alice@test", pass: secret}
		srv := startLoggedAuthServer(t, sess, sink)
		tr, tconn := dialTLS(t, srv.addr, srv.certPath)

		badCred := base64.StdEncoding.EncodeToString([]byte("\x00alice@test\x00" + secret + "-bad"))
		send(t, tconn, "AUTH PLAIN "+badCred+"\r\n")
		expect(t, tr, 535)

		goodCred := base64.StdEncoding.EncodeToString([]byte("\x00alice@test\x00" + secret))
		send(t, tconn, "AUTH PLAIN "+goodCred+"\r\n")
		expect(t, tr, 235)

		assertNoSecretInEvents(t, sink.snapshot(), secret, badCred, goodCred)
	})
}

type failingBackend struct{ err error }

func (b failingBackend) NewSession(string) (Session, error) { return nil, b.err }

func startLoggedAuthServer(t *testing.T, sess Session, sink *captureSink) testTLSServer {
	t.Helper()
	certPath, keyPath, err := tlstest.SelfSigned(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := (&config.Config{TLSCert: certPath, TLSKey: keyPath}).TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Backend:   sessionBackend{sess},
		Hostname:  "mail.test",
		TLSConfig: tlsCfg,
		Logger:    logging.New(sink),
	}
	ln := serveOn(t, srv)
	return testTLSServer{addr: ln.Addr().String(), certPath: certPath}
}

func assertNoSecretInEvents(t *testing.T, events []logging.Event, secrets ...string) {
	t.Helper()
	for _, e := range events {
		dump := fmt.Sprintf("%s %s %v", e.Name, e.Err, e.Fields)
		for _, sec := range secrets {
			if strings.Contains(dump, sec) {
				t.Errorf("secret %q leaked into log event %+v", sec, e)
			}
		}
	}
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
