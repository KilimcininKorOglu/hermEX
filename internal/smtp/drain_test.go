package smtp

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strings"
	"testing"

	"hermex/internal/logging"
)

// failAfter yields its text and then a read error, like a connection that drops
// in the middle of a message body.
type failAfter struct {
	r   io.Reader
	err error
}

func (f *failAfter) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err == io.EOF {
		return n, f.err
	}
	return n, err
}

// refusingSession rejects every message without reading its body, so only the
// drain in consumeData reads the rest of the stream.
type refusingSession struct{ fakeSession }

func (s *refusingSession) Data(io.Reader) error { return errors.New("refused") }

// TestConsumeDataReportsABodyItCouldNotDrain follows RFC 5321 section 4.1.1.4: a
// body ends only at its "." line, and until that line is read the next bytes are
// message text, not commands. When the stream fails before the terminator the
// body must be reported unreadable, so the session ends instead of running the
// rest of the message as SMTP commands.
func TestConsumeDataReportsABodyItCouldNotDrain(t *testing.T) {
	broken := errors.New("connection reset")
	src := &failAfter{r: strings.NewReader("Subject: x\r\n\r\nRCPT TO:<victim@test>\r\n"), err: broken}
	tp := textproto.NewReader(bufio.NewReader(src))

	err := (&Server{}).consumeData(tp, &refusingSession{}, "")
	if !errors.Is(err, errBodyUnreadable) || !errors.Is(err, broken) {
		t.Fatalf("consumeData = %v, want errBodyUnreadable wrapping the read error", err)
	}
}

// readingSession reads the whole body the way a delivering session does and
// answers with the read's own error.
type readingSession struct {
	fakeSession
	got []byte
}

func (s *readingSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	s.got = b
	return err
}

// TestTruncatedBodyIsNeverDeliveredWhole follows RFC 5321 section 4.1.1.4: a
// message is complete only at its "." line. A client that disconnects in the
// middle of DATA, cleanly or with a reset, must hand the delivering session a read
// error, so a cut-off message is refused rather than filed as a whole one.
func TestTruncatedBodyIsNeverDeliveredWhole(t *testing.T) {
	resetErr := errors.New("connection reset")
	cases := []struct {
		name    string
		src     io.Reader
		wantErr error
	}{
		{"clean close mid-line", strings.NewReader("Subject: x\r\n\r\npartial"), io.ErrUnexpectedEOF},
		{"clean close at a line", strings.NewReader("Subject: x\r\n\r\nline\r\n"), io.ErrUnexpectedEOF},
		{"reset", &failAfter{r: strings.NewReader("Subject: x\r\n\r\nline\r\n"), err: resetErr}, resetErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := &readingSession{}
			tp := textproto.NewReader(bufio.NewReader(tc.src))
			err := (&Server{}).consumeData(tp, sess, "")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("consumeData err = %v, want %v (body=%q)", err, tc.wantErr, sess.got)
			}
			if !errors.Is(err, errBodyUnreadable) {
				t.Fatalf("consumeData err = %v, want errBodyUnreadable", err)
			}
		})
	}
}

// failingLogout is a session whose teardown fails, the way a store that cannot
// flush on close does.
type failingLogout struct{ fakeSession }

func (s *failingLogout) Logout() error { return errors.New("flush failed") }

// TestSessionTeardownFailureIsRecorded proves a session whose Logout fails leaves
// a session.logout.fail event, so an operator sees a teardown that lost work
// instead of a clean disconnect.
func TestSessionTeardownFailureIsRecorded(t *testing.T) {
	sink := &captureSink{}
	srv := &Server{Backend: sessionBackend{&failingLogout{}}, Hostname: "mail.test", Logger: logging.New(sink)}
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() { srv.handle(server); close(done) }()

	r := textproto.NewReader(bufio.NewReader(client))
	expect(t, r, 220)
	send(t, client, "QUIT\r\n")
	expect(t, r, 221)
	<-done
	mustClose(t, client)

	e, ok := findEvent(sink.snapshot(), "session.logout.fail")
	if !ok || e.Fields["error"] != "flush failed" {
		t.Fatalf("events = %+v, want a session.logout.fail naming the error", sink.snapshot())
	}
}

// TestConsumeDataKeepsTheDeliveryVerdict proves a body read to its terminator
// returns the session's own answer unchanged, and leaves the stream positioned
// at the next command line.
func TestConsumeDataKeepsTheDeliveryVerdict(t *testing.T) {
	tp := textproto.NewReader(bufio.NewReader(strings.NewReader("Subject: x\r\n\r\nbody\r\n.\r\nNOOP\r\n")))

	err := (&Server{}).consumeData(tp, &refusingSession{}, "")
	if err == nil || errors.Is(err, errBodyUnreadable) || err.Error() != "refused" {
		t.Fatalf("consumeData = %v, want the session's refusal", err)
	}
	next, err := tp.ReadLine()
	if err != nil {
		t.Fatalf("read command after drained DATA: %v", err)
	}
	wantEq(t, next, "NOOP", "command after drained body")
}

// TestUnreadableBodyClosesConnectionWithoutSmuggling verifies that when a client
// drops mid-DATA with smuggled SMTP commands in the body, the server logs
// message.read.fail, closes the connection, and never executes the smuggled command.
func TestUnreadableBodyClosesConnectionWithoutSmuggling(t *testing.T) {
	sink := &captureSink{}
	sess := &fakeSession{}
	srv := &Server{Backend: sessionBackend{sess}, Hostname: "mail.test", Logger: logging.New(sink)}
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() { srv.handle(server); close(done) }()

	r := textproto.NewReader(bufio.NewReader(client))
	expect(t, r, 220)
	send(t, client, "EHLO client.test\r\n")
	expect(t, r, 250)
	send(t, client, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	send(t, client, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 250)
	send(t, client, "DATA\r\n")
	expect(t, r, 354)
	send(t, client, "Subject: x\r\n\r\nRCPT TO:<victim@test>\r\n")
	mustClose(t, client)
	<-done

	wantEq(t, sess.delivered, 0, "delivered messages on truncated DATA")
	wantEq(t, len(sess.rcpts), 1, "recipients recorded before drop")
	wantEq(t, sess.rcpts[0], "bob@test", "only original recipient")
	if !hasEvent(sink.snapshot(), "message.read.fail") {
		t.Error("expected message.read.fail event")
	}
}
