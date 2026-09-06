package smtp

import (
	"bufio"
	"net"
	"strings"
	"testing"
)

// A transcript test compares the server's replies BYTE FOR BYTE, not by reply
// code. Every other test in this package asserts a code and lets the text drift,
// which leaves the wire surface unpinned: the enhanced status code, the reply
// text, the order of the EHLO capability lines and the "-"/" " continuation
// separator are all part of what a client parses, and all of them can change
// without a single code assertion noticing.
//
// This exists so the command loop can be restructured with proof that nothing a
// client reads moved.

// transcript drives one session and collects each reply verbatim.
type transcript struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
}

// newTranscript opens a session against a server with the given size limit
// (0 disables it, which also omits the EHLO SIZE line).
func newTranscript(t *testing.T, maxSize int64) *transcript {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Backend: &fakeBackend{sess: &fakeSession{}}, Hostname: "mail.test"}
	srv.SetMaxSize(maxSize)
	go func() { _ = srv.Serve(ln) }()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); _ = ln.Close() })
	return &transcript{t: t, conn: conn, br: bufio.NewReader(conn)}
}

// readReply reads one complete reply: continuation lines (a "-" in the fourth
// column) up to the final line, joined by "\n" so a mismatch reads as one block.
func (x *transcript) readReply() string {
	x.t.Helper()
	var lines []string
	for {
		line, err := x.br.ReadString('\n')
		if err != nil {
			x.t.Fatalf("read reply: %v (so far %q)", err, lines)
		}
		line = strings.TrimRight(line, "\r\n")
		lines = append(lines, line)
		if len(line) < 4 || line[3] != '-' {
			return strings.Join(lines, "\n")
		}
	}
}

// exchange writes a client line and asserts the whole reply verbatim.
func (x *transcript) exchange(sent, want string) {
	x.t.Helper()
	if _, err := x.conn.Write([]byte(sent)); err != nil {
		x.t.Fatalf("write %q: %v", sent, err)
	}
	if got := x.readReply(); got != want {
		x.t.Errorf("after %q\n got: %s\nwant: %s", sent, got, want)
	}
}

// banner asserts the connection greeting.
func (x *transcript) banner(want string) {
	x.t.Helper()
	if got := x.readReply(); got != want {
		x.t.Errorf("banner\n got: %s\nwant: %s", got, want)
	}
}

// ehloCapabilities is the block greetEHLO emits before the optional SIZE,
// STARTTLS and AUTH lines, in its own order. The order is nothing a client may
// depend on, but pinning it is how a reordering shows up as a deliberate change
// rather than a silent one. Every line carries the "-" continuation separator;
// the last line of the whole reply is what gets the space, which is why the
// separator is decided by whether SIZE follows.
const ehloCapabilities = "250-mail.test Hello client.test\n" +
	"250-PIPELINING\n" +
	"250-8BITMIME\n" +
	"250-ENHANCEDSTATUSCODES\n" +
	"250-SMTPUTF8\n" +
	"250-CHUNKING\n" +
	"250-BINARYMIME\n" +
	"250-MT-PRIORITY\n"

// ehloReply is the whole reply when no size limit is configured: DSN is last.
const ehloReply = ehloCapabilities + "250 DSN"

// ehloReplyWithSize is the whole reply when a limit is configured: DSN becomes a
// continuation line and SIZE is last.
func ehloReplyWithSize(limit string) string {
	return ehloCapabilities + "250-DSN\n250 SIZE " + limit
}

// TestTranscriptServiceAndSequence pins the replies to the service commands, the
// unknown verb, and every sequence error the command loop can report.
func TestTranscriptServiceAndSequence(t *testing.T) {
	x := newTranscript(t, 0)
	x.banner("220 mail.test ESMTP hermEX")

	// Before any greeting: a mail command is a sequence error, a service command
	// is not (RFC 5321 §4.1.4).
	x.exchange("MAIL FROM:<alice@test>\r\n", "503 5.5.1 send HELO/EHLO first")
	x.exchange("VRFY bob@test\r\n", "252 2.1.5 Cannot VRFY user, but will accept message and attempt delivery")
	x.exchange("EXPN staff@test\r\n", "502 5.5.1 EXPN not available")
	x.exchange("HELP\r\n", "214 2.0.0 hermEX ESMTP; supported: HELO EHLO MAIL RCPT DATA BDAT RSET NOOP QUIT (RFC 5321)")
	x.exchange("FROBNICATE\r\n", "500 5.0.0 command not recognized")
	x.exchange("NOOP\r\n", "250 2.0.0 OK")

	x.exchange("EHLO client.test\r\n", ehloReply)
	x.exchange("HELO client.test\r\n", "250 2.0.0 mail.test")

	// RCPT before MAIL, and DATA before RCPT.
	x.exchange("RCPT TO:<bob@test>\r\n", "503 5.0.0 need MAIL before RCPT")
	x.exchange("MAIL FROM:<alice@test>\r\n", "250 2.0.0 OK")
	x.exchange("DATA\r\n", "503 5.0.0 need RCPT before DATA")

	x.exchange("QUIT\r\n", "221 2.0.0 mail.test closing connection")
}

// TestTranscriptParameterRejections pins the reply to every malformed MAIL or
// RCPT parameter the loop refuses before the backend is consulted.
func TestTranscriptParameterRejections(t *testing.T) {
	x := newTranscript(t, 64)
	x.banner("220 mail.test ESMTP hermEX")
	x.exchange("EHLO client.test\r\n", ehloReplyWithSize("64"))

	x.exchange("MAIL alice@test\r\n", "501 5.0.0 syntax: MAIL FROM:<address>")
	x.exchange("MAIL FROM:<alice@test> SIZE=1000\r\n", "552 5.3.4 message size exceeds limit")
	x.exchange("MAIL FROM:<alice@test> MT-PRIORITY=42\r\n", "501 5.5.2 syntax error in MT-PRIORITY parameter")
	x.exchange("MAIL FROM:<alice@test> RET=PARTIAL\r\n", "501 5.5.4 syntax error in DSN parameter")
	x.exchange("MAIL FROM:<posıta@örnek.test>\r\n", "550 5.6.7 non-ASCII address requires the SMTPUTF8 extension")

	x.exchange("MAIL FROM:<alice@test>\r\n", "250 2.0.0 OK")
	x.exchange("RCPT bob@test\r\n", "501 5.0.0 syntax: RCPT TO:<address>")
	x.exchange("RCPT TO:<bob@test> NOTIFY=SOMETIMES\r\n", "501 5.5.4 syntax error in DSN parameter")
	x.exchange("RCPT TO:<alıcı@örnek.test>\r\n", "553 5.6.7 non-ASCII address requires the SMTPUTF8 extension")
	x.exchange("QUIT\r\n", "221 2.0.0 mail.test closing connection")
}

// TestTranscriptDataPath pins the DATA exchange, including the bare 354
// intermediate (no enhanced code) and the size rejection after the body.
func TestTranscriptDataPath(t *testing.T) {
	x := newTranscript(t, 0)
	x.banner("220 mail.test ESMTP hermEX")
	x.exchange("EHLO client.test\r\n", ehloReply)
	x.exchange("MAIL FROM:<alice@test>\r\n", "250 2.0.0 OK")
	x.exchange("RCPT TO:<bob@test>\r\n", "250 2.0.0 OK")
	x.exchange("DATA\r\n", "354 end data with <CR><LF>.<CR><LF>")
	x.exchange("Subject: hi\r\n\r\nbody\r\n.\r\n", "250 2.0.0 OK")

	// DATA closed the transaction, so the next one starts from scratch.
	x.exchange("RCPT TO:<bob@test>\r\n", "503 5.0.0 need MAIL before RCPT")
	x.exchange("RSET\r\n", "250 2.0.0 OK")
	x.exchange("QUIT\r\n", "221 2.0.0 mail.test closing connection")
}

// TestTranscriptDataSizeRejection pins the over-limit body reply, which arrives
// after the client has already sent every byte.
func TestTranscriptDataSizeRejection(t *testing.T) {
	x := newTranscript(t, 64)
	x.banner("220 mail.test ESMTP hermEX")
	x.exchange("EHLO client.test\r\n", ehloReplyWithSize("64"))
	x.exchange("MAIL FROM:<alice@test>\r\n", "250 2.0.0 OK")
	x.exchange("RCPT TO:<bob@test>\r\n", "250 2.0.0 OK")
	x.exchange("DATA\r\n", "354 end data with <CR><LF>.<CR><LF>")
	x.exchange("Subject: hi\r\n\r\n"+strings.Repeat("x", 500)+"\r\n.\r\n", "552 5.0.0 message exceeds size limit")
	x.exchange("QUIT\r\n", "221 2.0.0 mail.test closing connection")
}

// TestTranscriptBDATPath pins the CHUNKING exchange: the octet count in a
// non-final chunk's reply, the sequence errors, and the poisoned-transaction
// reply an oversized chunk leaves behind.
func TestTranscriptBDATPath(t *testing.T) {
	x := newTranscript(t, 0)
	x.banner("220 mail.test ESMTP hermEX")
	x.exchange("EHLO client.test\r\n", ehloReply)

	// BDAT before MAIL/RCPT: the chunk is still read off the wire first, then
	// refused, or the next command read would parse message bytes.
	x.exchange("BDAT 5\r\nhello", "503 5.5.1 need MAIL and RCPT before BDAT")
	x.exchange("BDAT nonsense\r\n", "501 5.5.4 syntax: BDAT <chunk-size> [LAST]")

	x.exchange("MAIL FROM:<alice@test>\r\n", "250 2.0.0 OK")
	x.exchange("RCPT TO:<bob@test>\r\n", "250 2.0.0 OK")
	x.exchange("BDAT 6\r\nfirst ", "250 2.0.0 6 octets received")
	x.exchange("BDAT 5 LAST\r\nchunk", "250 2.0.0 OK")
	x.exchange("QUIT\r\n", "221 2.0.0 mail.test closing connection")
}

// TestTranscriptBDATPoisonedTransaction pins what an oversized chunk leaves
// behind: the transaction is refused until RSET, and DATA cannot rescue it.
func TestTranscriptBDATPoisonedTransaction(t *testing.T) {
	x := newTranscript(t, 8)
	x.banner("220 mail.test ESMTP hermEX")
	x.exchange("EHLO client.test\r\n", ehloReplyWithSize("8"))
	x.exchange("MAIL FROM:<alice@test>\r\n", "250 2.0.0 OK")
	x.exchange("RCPT TO:<bob@test>\r\n", "250 2.0.0 OK")

	x.exchange("BDAT 20\r\n"+strings.Repeat("x", 20), "552 5.3.4 message size exceeds limit")
	x.exchange("BDAT 3\r\nabc", "503 5.5.0 BDAT transaction failed; send RSET")
	x.exchange("DATA\r\n", "503 5.5.1 DATA not allowed after BDAT; send RSET")
	x.exchange("RSET\r\n", "250 2.0.0 OK")
	x.exchange("QUIT\r\n", "221 2.0.0 mail.test closing connection")
}

// TestTranscriptBinaryMIMERequiresBDAT pins the reply a BODY=BINARYMIME
// transaction gets when the client sends DATA instead of BDAT.
func TestTranscriptBinaryMIMERequiresBDAT(t *testing.T) {
	x := newTranscript(t, 0)
	x.banner("220 mail.test ESMTP hermEX")
	x.exchange("EHLO client.test\r\n", ehloReply)
	x.exchange("MAIL FROM:<alice@test> BODY=BINARYMIME\r\n", "250 2.0.0 OK")
	x.exchange("RCPT TO:<bob@test>\r\n", "250 2.0.0 OK")
	x.exchange("DATA\r\n", "503 5.5.1 BINARYMIME requires BDAT, not DATA")
	x.exchange("QUIT\r\n", "221 2.0.0 mail.test closing connection")
}

// TestTranscriptAuthUnavailable pins the AUTH replies on a session that offers
// no credentials check and a link with no TLS, the two refusals a client sees
// before it ever reaches a mechanism.
func TestTranscriptAuthUnavailable(t *testing.T) {
	x := newTranscript(t, 0)
	x.banner("220 mail.test ESMTP hermEX")
	x.exchange("EHLO client.test\r\n", ehloReply)
	x.exchange("AUTH PLAIN\r\n", "503 5.5.1 AUTH not available")
	x.exchange("STARTTLS\r\n", "502 5.0.0 STARTTLS not available")
	x.exchange("QUIT\r\n", "221 2.0.0 mail.test closing connection")
}
