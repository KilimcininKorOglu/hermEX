package smtp

import (
	"bufio"
	"errors"
	"net"
	"net/textproto"
	"strings"
	"testing"

	"hermex/internal/logging"
)

// errStorePath is the shape of a real store failure reaching the reply helpers:
// it names the mailbox directory on disk.
var errStorePath = errors.New("open /var/lib/hermex/mail/hermex.test/bob/objects.sqlite3: permission denied")

// dialLoggedServer starts a server with a capturing logger and returns the reader,
// the connection and the sink, so a test can assert both what went on the wire and
// what was recorded.
func dialLoggedServer(t *testing.T, sess *fakeSession) (*textproto.Reader, net.Conn, *captureSink) {
	t.Helper()
	sink := &captureSink{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Backend: &fakeBackend{sess: sess}, Hostname: "mail.test", Logger: logging.New(sink)}
	go func() { _ = srv.Serve(ln) }()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); _ = ln.Close() })
	return textproto.NewReader(bufio.NewReader(conn)), conn, sink
}

// readReply reads one reply and returns its code and text.
func readReply(t *testing.T, r *textproto.Reader) (int, string) {
	t.Helper()
	code, msg, err := r.ReadResponse(0)
	if err != nil {
		if _, ok := err.(*textproto.Error); !ok {
			t.Fatalf("read reply: %v", err)
		}
		te := err.(*textproto.Error)
		return te.Code, te.Msg
	}
	return code, msg
}

// greet drives the session to the point where RCPT is accepted.
func greet(t *testing.T, r *textproto.Reader, conn net.Conn) {
	t.Helper()
	expect(t, r, 220)
	send(t, conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
}

// TestRcptErrorDoesNotDiscloseInternals proves an unclassified Rcpt error is answered
// with a fixed string. Port 25 takes mail from unauthenticated peers, so a store or
// driver fault would otherwise hand the mailbox path on disk to any remote MTA that
// sends a message.
func TestRcptErrorDoesNotDiscloseInternals(t *testing.T) {
	sess := &fakeSession{rcptErr: errStorePath}
	r, conn, _ := dialLoggedServer(t, sess)
	greet(t, r, conn)

	send(t, conn, "RCPT TO:<bob@test>\r\n")
	code, msg := readReply(t, r)
	if code != 550 {
		t.Fatalf("reply code = %d, want 550", code)
	}
	for _, leak := range []string{"/var/lib/hermex", "objects.sqlite3", "permission denied"} {
		if strings.Contains(msg, leak) {
			t.Errorf("the reply carries internal detail %q: %q", leak, msg)
		}
	}
}

// TestRcptErrorIsRecorded proves the withheld error is not lost: it reaches the
// central log, so a sanitized rejection stays diagnosable.
func TestRcptErrorIsRecorded(t *testing.T) {
	sess := &fakeSession{rcptErr: errStorePath}
	r, conn, sink := dialLoggedServer(t, sess)
	greet(t, r, conn)

	send(t, conn, "RCPT TO:<bob@test>\r\n")
	readReply(t, r)

	e, ok := findEvent(sink.snapshot(), "session.error")
	if !ok {
		t.Fatal("the withheld error was not recorded, so it is lost entirely")
	}
	if !strings.Contains(e.Fields["reason"].(string), "objects.sqlite3") {
		t.Errorf("recorded reason = %v, want the full error", e.Fields["reason"])
	}
}

// TestRcptPermErrorReachesTheWire proves a business rejection still carries its own
// message. "no such user" is the text a bounce quotes back to the sender, so
// sanitizing it would cost every bounce its only useful line.
func TestRcptPermErrorReachesTheWire(t *testing.T) {
	sess := &fakeSession{rcptErr: &PermError{Message: "no such user <bob@test>"}}
	r, conn, _ := dialLoggedServer(t, sess)
	greet(t, r, conn)

	send(t, conn, "RCPT TO:<bob@test>\r\n")
	code, msg := readReply(t, r)
	if code != 550 {
		t.Fatalf("reply code = %d, want 550", code)
	}
	if !strings.Contains(msg, "no such user <bob@test>") {
		t.Errorf("reply = %q, want the rejection reason", msg)
	}
}

// TestDataErrorDoesNotDiscloseInternals proves the body path withholds internals too,
// while a PermError still carries its message and a TempError still defers.
func TestDataErrorDoesNotDiscloseInternals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		code     int
		wantText string
		noLeak   bool
	}{
		{"unclassified", errStorePath, 554, "", true},
		{"business", &PermError{Message: "5.2.2 mailbox is full"}, 554, "mailbox is full", false},
		{"transient", &TempError{Message: "4.3.0 retry later"}, 451, "retry later", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &fakeSession{dataErr: tc.err}
			r, conn, _ := dialLoggedServer(t, sess)
			greet(t, r, conn)
			send(t, conn, "RCPT TO:<bob@test>\r\n")
			expect(t, r, 250)
			send(t, conn, "DATA\r\n")
			expect(t, r, 354)
			send(t, conn, "Subject: hi\r\n\r\nbody\r\n.\r\n")

			code, msg := readReply(t, r)
			if code != tc.code {
				t.Fatalf("reply code = %d, want %d (%q)", code, tc.code, msg)
			}
			if tc.noLeak {
				for _, leak := range []string{"/var/lib/hermex", "objects.sqlite3", "permission denied"} {
					if strings.Contains(msg, leak) {
						t.Errorf("the reply carries internal detail %q: %q", leak, msg)
					}
				}
			}
			if tc.wantText != "" && !strings.Contains(msg, tc.wantText) {
				t.Errorf("reply = %q, want it to carry %q", msg, tc.wantText)
			}
		})
	}
}
