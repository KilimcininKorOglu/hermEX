package smtp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

// TestServerRcptTempErrorIsTemporary proves a backend TempError from Rcpt is
// reported as a 451 temporary failure (the sender retries) — the wire behaviour
// greylisting depends on.
func TestServerRcptTempErrorIsTemporary(t *testing.T) {
	sess := &fakeSession{rcptErr: &TempError{Message: "greylisted, retry later"}}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)
	fmt.Fprint(conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 451)
}

// TestServerRcptPermErrorIsPermanent proves an ordinary Rcpt error stays a 550
// permanent rejection.
func TestServerRcptPermErrorIsPermanent(t *testing.T) {
	sess := &fakeSession{rcptErr: errors.New("no such mailbox")}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)
	fmt.Fprint(conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 550)
}

// TestServerDataTempErrorIsTemporary proves a backend TempError from Data is
// reported as a 451 (the sender retries) and an ordinary Data error stays a 554
// permanent rejection. The 451 is the wire behaviour the antivirus
// scanner-unavailable path depends on.
func TestServerDataTempErrorIsTemporary(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"temp", &TempError{Message: "scanner unavailable, retry later"}, 451},
		{"perm", errors.New("delivery failed"), 554},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &fakeSession{dataErr: tc.err}
			r, conn := dialServer(t, sess)
			expect(t, r, 220)
			fmt.Fprint(conn, "EHLO client.test\r\n")
			expect(t, r, 250)
			fmt.Fprint(conn, "MAIL FROM:<alice@test>\r\n")
			expect(t, r, 250)
			fmt.Fprint(conn, "RCPT TO:<bob@test>\r\n")
			expect(t, r, 250)
			fmt.Fprint(conn, "DATA\r\n")
			expect(t, r, 354)
			fmt.Fprint(conn, "Subject: hi\r\n\r\nbody\r\n.\r\n")
			expect(t, r, tc.code)
		})
	}
}

type fakeSession struct {
	from    string
	rcpts   []string
	data    []byte
	rcptErr error // when set, Rcpt returns it (to exercise the error→reply mapping)
	dataErr error // when set, Data returns it (to exercise the DATA error→reply mapping)
}

func (s *fakeSession) Mail(from string) error { s.from = from; return nil }
func (s *fakeSession) Rcpt(to string) error {
	if s.rcptErr != nil {
		return s.rcptErr
	}
	s.rcpts = append(s.rcpts, to)
	return nil
}
func (s *fakeSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	s.data = b
	if err != nil {
		return err
	}
	return s.dataErr
}
func (s *fakeSession) Reset()        { s.from = ""; s.rcpts = nil }
func (s *fakeSession) Logout() error { return nil }

type fakeBackend struct{ sess *fakeSession }

func (b *fakeBackend) NewSession(string) (Session, error) { return b.sess, nil }

func dialServer(t *testing.T, sess *fakeSession) (*textproto.Reader, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Backend: &fakeBackend{sess: sess}, Hostname: "mail.test"}
	go srv.Serve(ln)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); ln.Close() })
	return textproto.NewReader(bufio.NewReader(conn)), conn
}

func expect(t *testing.T, r *textproto.Reader, code int) {
	t.Helper()
	if _, _, err := r.ReadResponse(code); err != nil {
		t.Fatalf("expected %d: %v", code, err)
	}
}

func TestServerTransaction(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)

	expect(t, r, 220)
	fmt.Fprint(conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "RCPT TO:<carol@test>\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "DATA\r\n")
	expect(t, r, 354)
	// "..dotstuffed" on the wire must arrive as ".dotstuffed" after unstuffing.
	fmt.Fprint(conn, "Subject: hi\r\n\r\nline one\r\n..dotstuffed\r\n.\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "QUIT\r\n")
	expect(t, r, 221)

	if sess.from != "alice@test" {
		t.Errorf("from = %q, want alice@test", sess.from)
	}
	if len(sess.rcpts) != 2 || sess.rcpts[0] != "bob@test" || sess.rcpts[1] != "carol@test" {
		t.Errorf("rcpts = %v", sess.rcpts)
	}
	// Every accepted message is stamped with a Received: trace header (RFC 5321
	// §4.4) ahead of the body, recording the helo name, the resolved name and IP,
	// and the SMTP protocol. The dot-unstuffed body must then follow it intact.
	got := string(sess.data)
	if !strings.HasPrefix(got, "Received: from client.test (") {
		t.Errorf("data missing the Received: trace header: %q", got)
	}
	if !strings.Contains(got, "[127.0.0.1])") {
		t.Errorf("Received: header should record the client IP: %q", got)
	}
	if !strings.Contains(got, " with SMTP;") {
		t.Errorf("Received: header should record the SMTP protocol: %q", got)
	}
	body := "Subject: hi\r\n\r\nline one\r\n.dotstuffed\r\n"
	if !strings.HasSuffix(got, body) {
		t.Errorf("body after the trace header = %q, want it to end with %q", got, body)
	}
}

// TestServerEnforcesMaxSize proves the size limit set via SetMaxSize is both
// advertised (EHLO SIZE) and enforced (an over-limit message is rejected 552), the
// hook the MTA's poll drives so an operator's edit applies without a restart.
// TestServerServiceCommands proves the RFC 5321 service commands return their
// proper codes instead of the 500 of an unrecognized command: VRFY answers the
// privacy-preserving 252 (never confirming an address, §7.3), EXPN is disabled
// with 502 (recognized-but-unimplemented, §4.2.4), and HELP gives a 214.
func TestServerServiceCommands(t *testing.T) {
	r, conn := dialServer(t, &fakeSession{})
	expect(t, r, 220)
	fmt.Fprint(conn, "EHLO client.test\r\n")
	expect(t, r, 250)

	fmt.Fprint(conn, "VRFY bob@test\r\n")
	expect(t, r, 252)
	fmt.Fprint(conn, "EXPN staff@test\r\n")
	expect(t, r, 502)
	fmt.Fprint(conn, "HELP\r\n")
	expect(t, r, 214)
	// A genuinely unknown verb still falls through to 500.
	fmt.Fprint(conn, "FROBNICATE\r\n")
	expect(t, r, 500)
}

func TestServerEnforcesMaxSize(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Backend: &fakeBackend{sess: &fakeSession{}}, Hostname: "mail.test"}
	srv.SetMaxSize(64) // 64 bytes
	go srv.Serve(ln)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); ln.Close() })
	r := textproto.NewReader(bufio.NewReader(conn))

	expect(t, r, 220)
	fmt.Fprint(conn, "EHLO client.test\r\n")
	_, msg, err := r.ReadResponse(250)
	if err != nil {
		t.Fatalf("EHLO: %v", err)
	}
	if !strings.Contains(msg, "SIZE 64") {
		t.Errorf("EHLO did not advertise the configured size limit: %q", msg)
	}
	fmt.Fprint(conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "DATA\r\n")
	expect(t, r, 354)
	// A body well past 64 bytes is rejected with 552, not accepted.
	fmt.Fprint(conn, "Subject: hi\r\n\r\n"+strings.Repeat("x", 500)+"\r\n.\r\n")
	expect(t, r, 552)
}

// TestBuildReceived covers the reference Received form: the helo name plus the
// reverse-DNS name and client IP in the from-clause, the SMTP/SMTPS "with" token
// (SMTPS only over TLS — neither EHLO nor AUTH is recorded there), an empty helo or
// rDNS degrading to "unknown", and an IPv6 client address carrying the "IPv6:" tag.
func TestBuildReceived(t *testing.T) {
	when := time.Date(2026, 6, 19, 22, 9, 21, 0, time.FixedZone("", 3*3600))
	cases := []struct {
		name               string
		helo, rdns, remote string
		tls                bool
		wantFrom, wantTok  string
	}{
		{"plain", "client.test", "mx.client.test", "198.51.100.7:54321", false, "from client.test (mx.client.test [198.51.100.7])", "with SMTP;"},
		{"tls", "client.test", "mx.client.test", "198.51.100.7:54321", true, "from client.test (mx.client.test [198.51.100.7])", "with SMTPS;"},
		{"no-helo", "", "mx.client.test", "198.51.100.7:54321", false, "from unknown (mx.client.test [198.51.100.7])", "with SMTP;"},
		{"no-rdns", "client.test", "", "198.51.100.7:54321", false, "from client.test (unknown [198.51.100.7])", "with SMTP;"},
		{"ipv6", "client.test", "mx.client.test", "[2001:db8::1]:54321", false, "from client.test (mx.client.test [IPv6:2001:db8::1])", "with SMTP;"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildReceived(c.helo, c.remote, c.rdns, "mail.hermex.test", c.tls, when)
			if !strings.HasPrefix(got, "Received: "+c.wantFrom) {
				t.Errorf("from clause = %q, want prefix %q", got, "Received: "+c.wantFrom)
			}
			if !strings.Contains(got, c.wantTok) {
				t.Errorf("protocol token: got %q, want %q", got, c.wantTok)
			}
			if !strings.Contains(got, "by mail.hermex.test ") {
				t.Errorf("missing the by-hostname clause: %q", got)
			}
			if !strings.HasSuffix(got, "Fri, 19 Jun 2026 22:09:21 +0300\r\n") {
				t.Errorf("date tail = %q, want the RFC 5322 date", got)
			}
		})
	}
}

func TestServerSequencingAndSyntax(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)

	expect(t, r, 220)
	fmt.Fprint(conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	// RCPT before MAIL is a bad sequence.
	fmt.Fprint(conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 503)
	// Malformed MAIL argument is a syntax error.
	fmt.Fprint(conn, "MAIL FROM:alice@test\r\n")
	expect(t, r, 501)
	fmt.Fprint(conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	// DATA before any RCPT is a bad sequence.
	fmt.Fprint(conn, "DATA\r\n")
	expect(t, r, 503)
	fmt.Fprint(conn, "RSET\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "FOOBAR\r\n")
	expect(t, r, 500)
	fmt.Fprint(conn, "QUIT\r\n")
	expect(t, r, 221)
}
