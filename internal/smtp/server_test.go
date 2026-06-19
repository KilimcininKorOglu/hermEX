package smtp

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

type fakeSession struct {
	from  string
	rcpts []string
	data  []byte
}

func (s *fakeSession) Mail(from string) error { s.from = from; return nil }
func (s *fakeSession) Rcpt(to string) error   { s.rcpts = append(s.rcpts, to); return nil }
func (s *fakeSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	s.data = b
	return err
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
	// §4.4) ahead of the body, recording the EHLO name and the client IP. The
	// dot-unstuffed body must then follow it intact.
	got := string(sess.data)
	if !strings.HasPrefix(got, "Received: from client.test ([") {
		t.Errorf("data missing the Received: trace header: %q", got)
	}
	if !strings.Contains(got, " with ESMTP;") {
		t.Errorf("Received: header should record the ESMTP protocol: %q", got)
	}
	body := "Subject: hi\r\n\r\nline one\r\n.dotstuffed\r\n"
	if !strings.HasSuffix(got, body) {
		t.Errorf("body after the trace header = %q, want it to end with %q", got, body)
	}
}

// TestBuildReceived covers the RFC 3848 "with" protocol token across greeting and
// security states, plus the header shape: the EHLO name and client IP are
// recorded, an empty HELO degrades to "unknown", and the host:port is reduced to
// the bare IP. hermEX requires TLS before AUTH, so an authenticated session is
// always ESMTPSA, never bare ESMTPA.
func TestBuildReceived(t *testing.T) {
	when := time.Date(2026, 6, 19, 22, 9, 21, 0, time.FixedZone("", 3*3600))
	cases := []struct {
		name               string
		helo               string
		esmtp, tls, authed bool
		wantFrom, wantTok  string
	}{
		{"helo", "client.test", false, false, false, "from client.test ([198.51.100.7])", "with SMTP;"},
		{"ehlo", "client.test", true, false, false, "from client.test ([198.51.100.7])", "with ESMTP;"},
		{"ehlo+tls", "client.test", true, true, false, "from client.test ([198.51.100.7])", "with ESMTPS;"},
		{"ehlo+tls+auth", "client.test", true, true, true, "from client.test ([198.51.100.7])", "with ESMTPSA;"},
		{"no-helo", "", true, false, false, "from unknown ([198.51.100.7])", "with ESMTP;"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildReceived(c.helo, "198.51.100.7:54321", "mail.hermex.test", c.esmtp, c.tls, c.authed, when)
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
