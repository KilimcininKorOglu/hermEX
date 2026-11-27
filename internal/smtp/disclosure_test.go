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
	srv := &Server{Backend: &fakeBackend{sess: sess}, Hostname: "mail.test", Logger: logging.New(sink)}
	ln := serveOn(t, srv)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustClose(t, conn) })
	return textproto.NewReader(bufio.NewReader(conn)), conn, sink
}

// readReply reads one reply and returns its code and text.
func readReply(t *testing.T, r *textproto.Reader) (int, string) {
	t.Helper()
	code, msg, err := r.ReadResponse(0)
	if err != nil {
		var te *textproto.Error
		if !errors.As(err, &te) {
			t.Fatalf("read reply: %v", err)
		}
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

func assertNoInternalLeak(t *testing.T, msg string) {
	t.Helper()
	for _, leak := range []string{"/var/lib/hermex", "objects.sqlite3", "permission denied"} {
		if strings.Contains(msg, leak) {
			t.Errorf("the reply carries internal detail %q: %q", leak, msg)
		}
	}
}

// TestRcptErrorDoesNotDiscloseInternals proves an unclassified Rcpt error is answered
// with a fixed string, records no recipient, and logs the withheld error.
func TestRcptErrorDoesNotDiscloseInternals(t *testing.T) {
	sess := &fakeSession{rcptErr: errStorePath}
	r, conn, sink := dialLoggedServer(t, sess)
	greet(t, r, conn)

	send(t, conn, "RCPT TO:<bob@test>\r\n")
	code, msg := readReply(t, r)
	if code != 550 {
		t.Fatalf("reply code = %d, want 550", code)
	}
	assertNoInternalLeak(t, msg)
	wantEq(t, len(sess.rcpts), 0, "accepted recipients after refused RCPT")
	send(t, conn, "DATA\r\n")
	expect(t, r, 503)
	if !hasEvent(sink.snapshot(), "session.error") {
		t.Error("expected session.error event in log")
	}
}

// TestRcptErrorIsRecorded proves the withheld error is not lost: it reaches the
// central log, so a sanitized rejection stays diagnosable.
func TestRcptErrorIsRecorded(t *testing.T) {
	sess := &fakeSession{rcptErr: errStorePath}
	r, conn, sink := dialLoggedServer(t, sess)
	greet(t, r, conn)

	send(t, conn, "RCPT TO:<bob@test>\r\n")
	code, msg := readReply(t, r)
	wantEq(t, code, 550, "RCPT reply code")
	assertNoInternalLeak(t, msg)

	e, ok := findEvent(sink.snapshot(), "session.error")
	if !ok {
		t.Fatal("the withheld error was not recorded, so it is lost entirely")
	}
	reason, ok := e.Fields["reason"].(string)
	if !ok || !strings.Contains(reason, "objects.sqlite3") {
		t.Errorf("recorded reason = %v (ok=%v), want the full error", e.Fields["reason"], ok)
	}
}

// TestRcptPermErrorReachesTheWire proves a business rejection still carries its own
// message and leaves no recipient recorded or internal error logged.
func TestRcptPermErrorReachesTheWire(t *testing.T) {
	sess := &fakeSession{rcptErr: &PermError{Message: "no such user <bob@test>"}}
	r, conn, sink := dialLoggedServer(t, sess)
	greet(t, r, conn)

	send(t, conn, "RCPT TO:<bob@test>\r\n")
	code, msg := readReply(t, r)
	if code != 550 {
		t.Fatalf("reply code = %d, want 550", code)
	}
	if !strings.Contains(msg, "no such user <bob@test>") {
		t.Errorf("reply = %q, want the rejection reason", msg)
	}
	wantEq(t, len(sess.rcpts), 0, "accepted recipients after PermError")
	if hasEvent(sink.snapshot(), "session.error") {
		t.Error("unexpected session.error logged for PermError")
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
			r, conn, sink := dialLoggedServer(t, sess)
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
				assertNoInternalLeak(t, msg)
			}
			if tc.wantText != "" && !strings.Contains(msg, tc.wantText) {
				t.Errorf("reply = %q, want it to carry %q", msg, tc.wantText)
			}
			wantEq(t, sess.delivered, 0, "delivered messages after Data error")
			send(t, conn, "RCPT TO:<bob@test>\r\n")
			expect(t, r, 503)
			e, ok := findEvent(sink.snapshot(), "message.reject")
			if !ok {
				t.Fatal("expected message.reject event")
			}
			if tc.noLeak {
				reason, ok := e.Fields["reason"].(string)
				if !ok || !strings.Contains(reason, "objects.sqlite3") {
					t.Errorf("message.reject reason = %v, want internal detail logged", e.Fields["reason"])
				}
			}
		})
	}
}

// TestMailAndBDATErrorsDoNotDiscloseInternals verifies unclassified Mail and BDAT
// errors withhold internal details on the wire, log them, and leave state clean.
func TestMailAndBDATErrorsDoNotDiscloseInternals(t *testing.T) {
	t.Run("mail error", func(t *testing.T) {
		sess := &fakeSession{mailErr: errStorePath}
		r, conn, sink := dialLoggedServer(t, sess)
		expect(t, r, 220)
		send(t, conn, "EHLO client.test\r\n")
		expect(t, r, 250)
		send(t, conn, "MAIL FROM:<alice@test>\r\n")
		code, msg := readReply(t, r)
		wantEq(t, code, 550, "MAIL error reply code")
		assertNoInternalLeak(t, msg)
		wantEq(t, sess.from, "", "sender after refused MAIL")
		send(t, conn, "RCPT TO:<bob@test>\r\n")
		expect(t, r, 503)
		if !hasEvent(sink.snapshot(), "session.error") {
			t.Error("expected session.error event for refused MAIL")
		}
	})

	t.Run("bdat error", func(t *testing.T) {
		sess := &fakeSession{dataErr: errStorePath}
		r, conn, sink := dialLoggedServer(t, sess)
		greet(t, r, conn)
		send(t, conn, "RCPT TO:<bob@test>\r\n")
		expect(t, r, 250)
		send(t, conn, "BDAT 4 LAST\r\nbody")
		code, msg := readReply(t, r)
		wantEq(t, code, 554, "BDAT error reply code")
		assertNoInternalLeak(t, msg)
		wantEq(t, sess.delivered, 0, "delivered messages after BDAT error")
		if !hasEvent(sink.snapshot(), "message.reject") {
			t.Error("expected message.reject event for BDAT error")
		}
	})
}
