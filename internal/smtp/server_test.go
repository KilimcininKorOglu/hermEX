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
// reported as a 451 temporary failure (the sender retries), leaving the
// recipient list empty.
func TestServerRcptTempErrorIsTemporary(t *testing.T) {
	sess := &fakeSession{rcptErr: &TempError{Message: "greylisted, retry later"}}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)
	send(t, conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 451)
	wantEq(t, len(sess.rcpts), 0, "recipients after 451")
	send(t, conn, "DATA\r\n")
	expect(t, r, 503)
}

// TestServerRcptPermErrorIsPermanent proves an ordinary Rcpt error stays a 550
// permanent rejection, leaving the recipient list empty.
func TestServerRcptPermErrorIsPermanent(t *testing.T) {
	sess := &fakeSession{rcptErr: errors.New("no such mailbox")}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)
	send(t, conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 550)
	wantEq(t, len(sess.rcpts), 0, "recipients after 550")
	send(t, conn, "DATA\r\n")
	expect(t, r, 503)
}

// TestServerDataTempErrorIsTemporary proves a backend TempError from Data is
// reported as a 451 (the sender retries) and an ordinary Data error stays a 554
// permanent rejection, leaving delivered count at 0 and resetting the transaction.
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
			send(t, conn, "EHLO client.test\r\n")
			expect(t, r, 250)
			send(t, conn, "MAIL FROM:<alice@test>\r\n")
			expect(t, r, 250)
			send(t, conn, "RCPT TO:<bob@test>\r\n")
			expect(t, r, 250)
			send(t, conn, "DATA\r\n")
			expect(t, r, 354)
			send(t, conn, "Subject: hi\r\n\r\nbody\r\n.\r\n")
			expect(t, r, tc.code)
			wantEq(t, sess.delivered, 0, "delivered messages on failure")
			send(t, conn, "RCPT TO:<bob@test>\r\n")
			expect(t, r, 503)
		})
	}
}

type fakeSession struct {
	from      string
	rcpts     []string
	data      []byte
	delivered int
	lastMail  MailParams // the parameters of the most recent Mail call
	lastRcpt  RcptParams // the parameters of the most recent Rcpt call
	mailErr   error      // when set, Mail returns it
	rcptErr   error      // when set, Rcpt returns it (to exercise the error->reply mapping)
	dataErr   error      // when set, Data returns it (to exercise the DATA error->reply mapping)

	// dataFrom and dataRcpts record the envelope as each Data call saw it, one
	// entry per accepted message. The transaction boundary clears from and rcpts
	// afterwards, so a test about what a single message carried reads these rather
	// than the live fields.
	dataFrom  []string
	dataRcpts [][]string
}

func (s *fakeSession) Mail(from string, params MailParams) error {
	if s.mailErr != nil {
		return s.mailErr
	}
	s.from, s.lastMail = from, params
	return nil
}
func (s *fakeSession) Rcpt(to string, params RcptParams) error {
	s.lastRcpt = params
	if s.rcptErr != nil {
		return s.rcptErr
	}
	s.rcpts = append(s.rcpts, to)
	return nil
}
func (s *fakeSession) Data(r io.Reader) error {
	s.dataFrom = append(s.dataFrom, s.from)
	s.dataRcpts = append(s.dataRcpts, append([]string(nil), s.rcpts...))
	b, err := io.ReadAll(r)
	s.data = b
	if err != nil {
		return err
	}
	if s.dataErr != nil {
		return s.dataErr
	}
	s.delivered++
	return nil
}
func (s *fakeSession) Reset()        { s.from = ""; s.rcpts = nil }
func (s *fakeSession) Logout() error { return nil }

type fakeBackend struct{ sess *fakeSession }

func (b *fakeBackend) NewSession(string) (Session, error) { return b.sess, nil }

func dialServer(t *testing.T, sess *fakeSession) (*textproto.Reader, net.Conn) {
	t.Helper()
	return dialServerWithLimit(t, sess, 0)
}

func dialServerWithLimit(t *testing.T, sess *fakeSession, maxSize int64) (*textproto.Reader, net.Conn) {
	t.Helper()
	srv := &Server{Backend: &fakeBackend{sess: sess}, Hostname: "mail.test"}
	if maxSize > 0 {
		srv.SetMaxSize(maxSize)
	}
	ln := serveOn(t, srv)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustClose(t, conn) })
	return textproto.NewReader(bufio.NewReader(conn)), conn
}

// send writes a client line to the server connection, failing the test if the
// write does not complete: a broken write means the test setup is unusable.
func send(t *testing.T, c net.Conn, s string) {
	t.Helper()
	if _, err := fmt.Fprint(c, s); err != nil {
		t.Fatalf("write %q: %v", s, err)
	}
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
	send(t, conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<carol@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "DATA\r\n")
	expect(t, r, 354)
	// "..dotstuffed" on the wire must arrive as ".dotstuffed" after unstuffing.
	send(t, conn, "Subject: hi\r\n\r\nline one\r\n..dotstuffed\r\n.\r\n")
	expect(t, r, 250)

	// Null reverse-path (RFC 5321 §4.5.5 bounce sender) is also accepted.
	send(t, conn, "MAIL FROM:<>\r\n")
	expect(t, r, 250)
	send(t, conn, "QUIT\r\n")
	expect(t, r, 221)

	if len(sess.dataRcpts) != 1 {
		t.Fatalf("the server accepted %d messages, want 1", len(sess.dataRcpts))
	}
	if sess.dataFrom[0] != "alice@test" {
		t.Errorf("from = %q, want alice@test", sess.dataFrom[0])
	}
	if got := sess.dataRcpts[0]; len(got) != 2 || got[0] != "bob@test" || got[1] != "carol@test" {
		t.Errorf("rcpts = %v", got)
	}
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

// TestServerMailRequiresGreeting proves RFC 5321 §4.1.4: MAIL before any
// greeting is 503 with no transaction opened, while VRFY works without it.
func TestServerMailRequiresGreeting(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)

	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 503)
	wantEq(t, sess.from, "", "sender after pre-greeting MAIL")
	send(t, conn, "VRFY bob@test\r\n")
	expect(t, r, 252)
	send(t, conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
}

// TestServerServiceCommands proves the RFC 5321 service commands return their
// proper codes instead of 500.
func TestServerServiceCommands(t *testing.T) {
	r, conn := dialServer(t, &fakeSession{})
	expect(t, r, 220)
	send(t, conn, "EHLO client.test\r\n")
	expect(t, r, 250)

	send(t, conn, "VRFY bob@test\r\n")
	expect(t, r, 252)
	send(t, conn, "EXPN staff@test\r\n")
	expect(t, r, 502)
	send(t, conn, "HELP\r\n")
	expect(t, r, 214)
	send(t, conn, "FROBNICATE\r\n")
	expect(t, r, 500)
}

// TestServerEnforcesMaxSize proves the size limit set via SetMaxSize is advertised,
// accepts exact-limit bodies, and rejects over-limit bodies with 552.
func TestServerEnforcesMaxSize(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServerWithLimit(t, sess, 64)

	expect(t, r, 220)
	send(t, conn, "EHLO client.test\r\n")
	msg := readCodedReply(t, r, 250, "EHLO")
	if !strings.Contains(msg, "SIZE 64") {
		t.Errorf("EHLO did not advertise the configured size limit: %q", msg)
	}

	// Exact 64-byte body succeeds (15 + 47 + 2 = 64).
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "DATA\r\n")
	expect(t, r, 354)
	send(t, conn, "Subject: hi\r\n\r\n"+strings.Repeat("a", 47)+"\r\n.\r\n")
	expect(t, r, 250)
	wantEq(t, sess.delivered, 1, "delivered at exact 64-byte limit")

	// 65-byte body (15 + 48 + 2 = 65) is rejected with 552.
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "DATA\r\n")
	expect(t, r, 354)
	send(t, conn, "Subject: hi\r\n\r\n"+strings.Repeat("x", 48)+"\r\n.\r\n")
	expect(t, r, 552)
	wantEq(t, sess.delivered, 1, "delivered unchanged after 552")
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 503)
}

// TestServerSMTPUTF8 proves RFC 6531: EHLO advertises SMTPUTF8 and a MAIL FROM
// carrying the SMTPUTF8 keyword with a UTF-8 envelope address is accepted.
func TestServerSMTPUTF8(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)

	send(t, conn, "EHLO client.test\r\n")
	ehlo := readCodedReply(t, r, 250, "EHLO")
	if !strings.Contains(ehlo, "SMTPUTF8") {
		t.Errorf("EHLO did not advertise SMTPUTF8: %q", ehlo)
	}

	const utf8From = "bücher@例え.test"
	send(t, conn, "MAIL FROM:<"+utf8From+"> SMTPUTF8\r\n")
	expect(t, r, 250)
	wantEq(t, sess.from, utf8From, "UTF-8 sender")
}

// TestServerEnhancedStatusCodes proves RFC 2034: EHLO advertises
// ENHANCEDSTATUSCODES, 2xx/4xx/5xx replies carry RFC 3463 status codes, and the
// 220 banner and 354 DATA intermediate stay bare.
func TestServerEnhancedStatusCodes(t *testing.T) {
	r, conn := dialServer(t, &fakeSession{})
	banner := readCodedReply(t, r, 220, "banner")
	wantEq(t, banner, "mail.test ESMTP hermEX", "bare 220 banner")

	send(t, conn, "EHLO client.test\r\n")
	ehlo := readCodedReply(t, r, 250, "EHLO")
	if !strings.Contains(ehlo, "ENHANCEDSTATUSCODES") {
		t.Errorf("EHLO did not advertise ENHANCEDSTATUSCODES: %q", ehlo)
	}

	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	wantReplyPrefix(t, readCodedReply(t, r, 250, "MAIL"), "2.0.0 ", "MAIL enhanced code")

	send(t, conn, "DATA\r\n")
	wantReplyPrefix(t, readCodedReply(t, r, 503, "DATA before RCPT"), "5.0.0 ", "DATA 503 enhanced code")

	send(t, conn, "VRFY bob@test\r\n")
	wantReplyPrefix(t, readCodedReply(t, r, 252, "VRFY"), "2.1.5 ", "VRFY enhanced code")

	send(t, conn, "RCPT TO:<bob@test>\r\n")
	readCodedReply(t, r, 250, "RCPT")
	send(t, conn, "DATA\r\n")
	wantEq(t, readCodedReply(t, r, 354, "DATA intermediate"), "end data with <CR><LF>.<CR><LF>", "bare 354 intermediate")
	send(t, conn, "Subject: hi\r\n\r\nbody\r\n.\r\n")
	wantReplyPrefix(t, readCodedReply(t, r, 250, "DATA accept"), "2.0.0 ", "DATA accept enhanced code")
}

// TestServerCommandLineLimit proves RFC 5321 §4.5.3.1.4: a command line past
// 512 octets is rejected with 500 without changing greeted state, while an exact
// 512-octet command is accepted.
func TestServerCommandLineLimit(t *testing.T) {
	r, conn := dialServer(t, &fakeSession{})
	expect(t, r, 220)

	// 513 payload octets (5 + 508) exceeds DefaultMaxCommandLine (512).
	send(t, conn, "EHLO "+strings.Repeat("a", 508)+"\r\n")
	expect(t, r, 500)
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 503)

	// Exact 512 payload octets (5 + 507) is accepted.
	send(t, conn, "EHLO "+strings.Repeat("a", 507)+"\r\n")
	expect(t, r, 250)
}

// TestServerSizeDeclaration proves RFC 1870 early rejection: a MAIL FROM whose
// declared SIZE exceeds the advertised maximum is refused with 552 without
// opening a transaction, while the exact limit is accepted.
func TestServerSizeDeclaration(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServerWithLimit(t, sess, 1000)

	expect(t, r, 220)
	send(t, conn, "EHLO client.test\r\n")
	expect(t, r, 250)

	for _, bad := range []string{"5000", "1001"} {
		send(t, conn, "MAIL FROM:<alice@test> SIZE="+bad+"\r\n")
		expect(t, r, 552)
		wantEq(t, sess.from, "", "sender after SIZE="+bad)
		send(t, conn, "RCPT TO:<bob@test>\r\n")
		expect(t, r, 503)
	}

	send(t, conn, "MAIL FROM:<alice@test> SIZE=1000\r\n")
	expect(t, r, 250)
	send(t, conn, "RSET\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
}

// TestBuildReceived covers the Received form: helo name, rDNS and IP in the
// from-clause, SMTP/SMTPS protocol token, unknown degradation, and IPv6 tag.
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

// TestParseBDAT covers the "BDAT <chunk-size> [LAST]" grammar (RFC 3030).
func TestParseBDAT(t *testing.T) {
	cases := []struct {
		arg      string
		size     int64
		last, ok bool
	}{
		{"0", 0, false, true},
		{"5", 5, false, true},
		{"0 LAST", 0, true, true},
		{"1024 last", 1024, true, true},
		{"", 0, false, false},
		{"-1", 0, false, false},
		{"99999999999999999999999999", 0, false, false},
		{"abc", 0, false, false},
		{"5 NOTLAST", 0, false, false},
		{"5 LAST EXTRA", 0, false, false},
		{"10 LAST\tEXTRA", 0, false, false},
	}
	for _, c := range cases {
		size, last, ok := parseBDAT(c.arg)
		if size != c.size || last != c.last || ok != c.ok {
			t.Errorf("parseBDAT(%q) = (%d,%v,%v), want (%d,%v,%v)", c.arg, size, last, ok, c.size, c.last, c.ok)
		}
	}
}

// TestServerBDATChunking proves RFC 3030 CHUNKING end to end.
func TestServerBDATChunking(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)

	send(t, conn, "EHLO client.test\r\n")
	ehlo := readCodedReply(t, r, 250, "EHLO")
	if !strings.Contains(ehlo, "CHUNKING") || !strings.Contains(ehlo, "BINARYMIME") {
		t.Errorf("EHLO did not advertise CHUNKING/BINARYMIME: %q", ehlo)
	}

	send(t, conn, "MAIL FROM:<alice@test> BODY=BINARYMIME\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 250)

	send(t, conn, "BDAT 6\r\n.Hello")
	expect(t, r, 250)
	send(t, conn, "BDAT 7 LAST\r\n, World")
	expect(t, r, 250)

	got := string(sess.data)
	if !strings.HasPrefix(got, "Received: from client.test (") {
		t.Errorf("BDAT body missing the Received: trace header: %q", got)
	}
	if !strings.HasSuffix(got, ".Hello, World") {
		t.Errorf("assembled BDAT body = %q, want it to end with %q", got, ".Hello, World")
	}
	if len(sess.dataRcpts) != 1 || sess.dataFrom[0] != "alice@test" ||
		len(sess.dataRcpts[0]) != 1 || sess.dataRcpts[0][0] != "bob@test" {
		t.Errorf("envelope not captured: from=%v rcpts=%v", sess.dataFrom, sess.dataRcpts)
	}
}

// TestServerBDATSequencing proves the RFC 3030 mixing rules: DATA after BDAT
// and DATA on a BODY=BINARYMIME transaction are refused 503 without delivering.
func TestServerBDATSequencing(t *testing.T) {
	t.Run("data after bdat", func(t *testing.T) {
		sess := &fakeSession{}
		r, conn := dialServer(t, sess)
		expect(t, r, 220)
		send(t, conn, "EHLO client.test\r\n")
		expect(t, r, 250)
		send(t, conn, "MAIL FROM:<alice@test>\r\n")
		expect(t, r, 250)
		send(t, conn, "RCPT TO:<bob@test>\r\n")
		expect(t, r, 250)
		send(t, conn, "BDAT 5\r\nHello")
		expect(t, r, 250)
		send(t, conn, "DATA\r\n")
		expect(t, r, 503)
		wantEq(t, sess.delivered, 0, "delivered after refused DATA")
		send(t, conn, "RSET\r\n")
		expect(t, r, 250)
	})
	t.Run("binarymime refuses data", func(t *testing.T) {
		sess := &fakeSession{}
		r, conn := dialServer(t, sess)
		expect(t, r, 220)
		send(t, conn, "EHLO client.test\r\n")
		expect(t, r, 250)
		send(t, conn, "MAIL FROM:<alice@test> BODY=BINARYMIME\r\n")
		expect(t, r, 250)
		send(t, conn, "RCPT TO:<bob@test>\r\n")
		expect(t, r, 250)
		send(t, conn, "DATA\r\n")
		expect(t, r, 503)
		wantEq(t, sess.delivered, 0, "delivered after BINARYMIME DATA")
	})
}

// TestServerBDATSizeLimit proves an over-limit BDAT chunk is rejected 552,
// poisons the transaction until RSET, and leaves the stream framed.
func TestServerBDATSizeLimit(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServerWithLimit(t, sess, 64)

	expect(t, r, 220)
	send(t, conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "BDAT 100\r\n"+strings.Repeat("x", 100))
	expect(t, r, 552)
	wantEq(t, sess.delivered, 0, "delivered after oversized BDAT")
	send(t, conn, "BDAT 4 LAST\r\ntest")
	expect(t, r, 503)
	wantEq(t, sess.delivered, 0, "delivered after poisoned BDAT")
	send(t, conn, "RSET\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<carol@test>\r\n")
	expect(t, r, 250)
}

// TestMTPriorityValid covers RFC 6710 §4.1 MT-PRIORITY validation.
func TestMTPriorityValid(t *testing.T) {
	cases := []struct {
		arg         string
		present, ok bool
	}{
		{"<a@b>", false, true},
		{"<a@b> MT-PRIORITY=0", true, true},
		{"<a@b> MT-PRIORITY=9", true, true},
		{"<a@b> MT-PRIORITY=-9", true, true},
		{"<a@b> SIZE=10 MT-PRIORITY=4 BODY=8BITMIME", true, true},
		{"<a@b> MT-PRIORITY=10", true, false},
		{"<a@b> MT-PRIORITY=-10", true, false},
		{"<a@b> MT-PRIORITY=99999999999999999999", true, false},
		{"<a@b> MT-PRIORITY=high", true, false},
		{"<a@b> MT-PRIORITY=", true, false},
		{"<a@b> MT-PRIORITY", true, false},
		{"<a@b> MT-PRIORITY=1 MT-PRIORITY=2", true, false},
		{"<a@b> mt-priority=1 MT-PRIORITY=2", true, false},
	}
	for _, c := range cases {
		present, ok := mtPriorityValid(c.arg)
		if present != c.present || ok != c.ok {
			t.Errorf("mtPriorityValid(%q) = (%v,%v), want (%v,%v)", c.arg, present, ok, c.present, c.ok)
		}
	}
}

// TestServerMTPriority proves RFC 6710 on the wire.
func TestServerMTPriority(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)

	send(t, conn, "EHLO client.test\r\n")
	ehlo := readCodedReply(t, r, 250, "EHLO")
	if !strings.Contains(ehlo, "MT-PRIORITY") {
		t.Errorf("EHLO did not advertise MT-PRIORITY: %q", ehlo)
	}

	send(t, conn, "MAIL FROM:<alice@test> MT-PRIORITY=4\r\n")
	expect(t, r, 250)
	send(t, conn, "RSET\r\n")
	expect(t, r, 250)

	for _, bad := range []string{"42", "urgent"} {
		send(t, conn, "MAIL FROM:<alice@test> MT-PRIORITY="+bad+"\r\n")
		expect(t, r, 501)
		wantEq(t, sess.from, "", "sender after MT-PRIORITY="+bad)
		send(t, conn, "RCPT TO:<bob@test>\r\n")
		expect(t, r, 503)
	}
}

// TestServerBoundsRecipientsPerTransaction verifies MaxRecipients enforcement.
func TestServerBoundsRecipientsPerTransaction(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)
	send(t, conn, "EHLO client.test\r\nMAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	expect(t, r, 250)
	for i := range MaxRecipients {
		send(t, conn, fmt.Sprintf("RCPT TO:<r%d@test>\r\n", i))
		expect(t, r, 250)
	}
	send(t, conn, "RCPT TO:<onemore@test>\r\n")
	code, msg, err := r.ReadResponse(0)
	if err != nil {
		t.Fatalf("read the reply to recipient %d: %v", MaxRecipients+1, err)
	}
	if code != 452 || !strings.HasPrefix(msg, "4.5.3") {
		t.Fatalf("recipient %d answered %d %q, want 452 4.5.3", MaxRecipients+1, code, msg)
	}
	wantEq(t, len(sess.rcpts), MaxRecipients, "recipients the session accepted")
	send(t, conn, "DATA\r\n")
	expect(t, r, 354)
	send(t, conn, "Subject: hi\r\n\r\nbody\r\n.\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<alice@test>\r\nRCPT TO:<again@test>\r\n")
	expect(t, r, 250)
	expect(t, r, 250)
}

func TestServerSequencingAndSyntax(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)

	expect(t, r, 220)
	send(t, conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 503)
	wantEq(t, len(sess.rcpts), 0, "rcpts after 503")
	send(t, conn, "MAIL FROM:alice@test\r\n")
	expect(t, r, 501)
	send(t, conn, "MAIL FROM:<alice@test\r\n")
	expect(t, r, 501)
	wantEq(t, sess.from, "", "sender after 501")

	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:bob@test\r\n")
	expect(t, r, 501)
	wantEq(t, len(sess.rcpts), 0, "rcpts after 501")
	send(t, conn, "DATA\r\n")
	expect(t, r, 503)
	wantEq(t, sess.delivered, 0, "delivered after 503")

	send(t, conn, "RSET\r\n")
	expect(t, r, 250)
	send(t, conn, "FOOBAR\r\n")
	expect(t, r, 500)
	send(t, conn, "QUIT\r\n")
	expect(t, r, 221)
}

func TestDSNMailParams(t *testing.T) {
	maxEnv := strings.Repeat("a", 100)
	tooLongEnv := strings.Repeat("a", 101)
	cases := []struct {
		arg   string
		ret   string
		envid string
		ok    bool
	}{
		{"<a@b>", "", "", true},
		{"<a@b> RET=FULL", "FULL", "", true},
		{"<a@b> RET=hdrs", "HDRS", "", true},
		{"<a@b> ENVID=abc123", "", "abc123", true},
		{"<a@b> ENVID=" + maxEnv, "", maxEnv, true},
		{"<a@b> RET=FULL ENVID=id+2Bx", "FULL", "id+2Bx", true},
		{"<a@b> RET=PARTIAL", "", "", false},
		{"<a@b> RET=FULL RET=HDRS", "", "", false},
		{"<a@b> ENVID=a ENVID=b", "", "", false},
		{"<a@b> ENVID=" + tooLongEnv, "", "", false},
		{"<a@b> ENVID=id+2bx", "", "", false},
		{"<a@b> ENVID=bad=value", "", "", false},
		{"<a@b> ENVID=trunc+2", "", "", false},
	}
	for _, c := range cases {
		p, ok := dsnMailParams(c.arg)
		if ok != c.ok || p.RET != c.ret || p.ENVID != c.envid {
			t.Errorf("dsnMailParams(%q) = (%+v,%v), want (RET=%q ENVID=%q,%v)", c.arg, p, ok, c.ret, c.envid, c.ok)
		}
	}
}

func TestDSNRcptParams(t *testing.T) {
	maxORCPT := "rfc822;" + strings.Repeat("x", 500-len("rfc822;"))
	tooLongORCPT := maxORCPT + "x"
	cases := []struct {
		arg    string
		notify string
		orcpt  string
		ok     bool
	}{
		{"<a@b>", "", "", true},
		{"<a@b> NOTIFY=NEVER", "NEVER", "", true},
		{"<a@b> NOTIFY=SUCCESS,FAILURE", "SUCCESS,FAILURE", "", true},
		{"<a@b> ORCPT=rfc822;bob@x", "", "rfc822;bob@x", true},
		{"<a@b> ORCPT=" + maxORCPT, "", maxORCPT, true},
		{"<a@b> NOTIFY=DELAY ORCPT=rfc822;c@x", "DELAY", "rfc822;c@x", true},
		{"<a@b> NOTIFY=NEVER,SUCCESS", "", "", false},
		{"<a@b> NOTIFY=SUCCESS,NEVER", "", "", false},
		{"<a@b> NOTIFY=SUCCESS,", "", "", false},
		{"<a@b> NOTIFY=MAYBE", "", "", false},
		{"<a@b> NOTIFY=", "", "", false},
		{"<a@b> NOTIFY=SUCCESS NOTIFY=DELAY", "", "", false},
		{"<a@b> ORCPT=bob@x", "", "", false},
		{"<a@b> ORCPT=;bob@x", "", "", false},
		{"<a@b> ORCPT=rfc(822);bob@x", "", "", false},
		{"<a@b> ORCPT=" + tooLongORCPT, "", "", false},
		{"<a@b> ORCPT=rfc822;bad=val", "", "", false},
	}
	for _, c := range cases {
		p, ok := dsnRcptParams(c.arg)
		if ok != c.ok || p.Notify != c.notify || p.ORCPT != c.orcpt {
			t.Errorf("dsnRcptParams(%q) = (%+v,%v), want (Notify=%q ORCPT=%q,%v)", c.arg, p, ok, c.notify, c.orcpt, c.ok)
		}
	}
}

func TestValidXtext(t *testing.T) {
	cases := []struct {
		s  string
		ok bool
	}{
		{"", true},
		{"abc", true},
		{"a+2Bb", true},
		{"a=b", false},
		{"a+b", false},
		{"a+2", false},
		{"a+2zb", false},
		{"a+2bb", false},
		{"a b", false},
		{"a\x00b", false},
		{"a\x7fb", false},
		{"aöb", false},
	}
	for _, c := range cases {
		if got := validXtext(c.s); got != c.ok {
			t.Errorf("validXtext(%q) = %v, want %v", c.s, got, c.ok)
		}
	}
}

// TestServerDSN proves RFC 3461 on the wire.
func TestServerDSN(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)

	send(t, conn, "EHLO client.test\r\n")
	ehlo := readCodedReply(t, r, 250, "EHLO")
	if !strings.Contains(ehlo, "DSN") {
		t.Errorf("EHLO did not advertise DSN: %q", ehlo)
	}

	send(t, conn, "MAIL FROM:<alice@test> RET=HDRS ENVID=id01\r\n")
	expect(t, r, 250)
	wantEq(t, sess.lastMail.RET, "HDRS", "MailParams.RET")
	wantEq(t, sess.lastMail.ENVID, "id01", "MailParams.ENVID")

	send(t, conn, "RCPT TO:<bob@test> NOTIFY=NEVER ORCPT=rfc822;bob@test\r\n")
	expect(t, r, 250)
	wantEq(t, sess.lastRcpt.Notify, "NEVER", "RcptParams.Notify")
	wantEq(t, sess.lastRcpt.ORCPT, "rfc822;bob@test", "RcptParams.ORCPT")

	send(t, conn, "RSET\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<alice@test> RET=PARTIAL\r\n")
	expect(t, r, 501)
	wantEq(t, sess.from, "", "sender after 501 RET=PARTIAL")
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 503)

	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<bob@test> NOTIFY=NEVER,SUCCESS\r\n")
	expect(t, r, 501)
	wantEq(t, len(sess.rcpts), 0, "rcpts after 501 NOTIFY")
	send(t, conn, "DATA\r\n")
	expect(t, r, 503)
}
