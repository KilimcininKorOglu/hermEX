package pop3

import (
	"bufio"
	"encoding/base64"
	"net"
	"net/textproto"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// TestPOP3AuthPlainInline checks AUTH PLAIN with an inline initial response logs in.
func TestPOP3AuthPlainInline(t *testing.T) {
	c := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
	send, wantOK := c.send, c.wantOK
	wantOK() // greeting
	send("AUTH PLAIN " + b64("\x00alice\x00secret"))
	wantOK() // authenticated, maildrop opened
	send("STAT")
	wantOK() // a TRANSACTION command now works
}

// TestPOP3AuthPlainContinuation checks AUTH PLAIN without an initial response: the
// server issues a "+ " continuation and reads the base64 credential.
func TestPOP3AuthPlainContinuation(t *testing.T) {
	c := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
	r, send, wantOK := c.r, c.send, c.wantOK
	wantOK() // greeting
	send("AUTH PLAIN")
	if l, err := r.ReadLine(); err != nil || !strings.HasPrefix(l, "+ ") {
		t.Fatalf("want a \"+ \" continuation, got %q (err %v)", l, err)
	}
	send(b64("\x00alice\x00secret"))
	wantOK()
}

// mustReadLine reads one reply line and fails the test when the read fails, so a
// closed connection is not read as an empty, mismatching line.
func mustReadLine(t *testing.T, r *textproto.Reader) string {
	t.Helper()
	l, err := r.ReadLine()
	if err != nil {
		t.Fatalf("read a reply line: %v", err)
	}
	return l
}

// TestPOP3AuthLogin checks the AUTH LOGIN username/password challenge exchange.
func TestPOP3AuthLogin(t *testing.T) {
	c := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
	r, send, wantOK := c.r, c.send, c.wantOK
	wantOK() // greeting
	send("AUTH LOGIN")
	if l := mustReadLine(t, r); l != "+ "+b64("Username:") {
		t.Fatalf("want a username challenge, got %q", l)
	}
	send(b64("alice"))
	if l := mustReadLine(t, r); l != "+ "+b64("Password:") {
		t.Fatalf("want a password challenge, got %q", l)
	}
	send(b64("secret"))
	wantOK()
}

// TestPOP3AuthList checks AUTH with no mechanism lists the supported ones.
func TestPOP3AuthList(t *testing.T) {
	c := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
	send, wantOK, readLines := c.send, c.wantOK, c.readLines
	wantOK() // greeting
	send("AUTH")
	wantOK()
	mechs := strings.Join(readLines(), " ")
	if !strings.Contains(mechs, "PLAIN") || !strings.Contains(mechs, "LOGIN") {
		t.Errorf("AUTH mechanism list = %q, want PLAIN and LOGIN", mechs)
	}
}

// TestPOP3AuthBadPassword checks a wrong password over AUTH PLAIN carries [AUTH].
func TestPOP3AuthBadPassword(t *testing.T) {
	c := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
	send, wantOK, wantERR := c.send, c.wantOK, c.wantERR
	wantOK() // greeting
	send("AUTH PLAIN " + b64("\x00alice\x00wrong"))
	if l := wantERR(); !strings.Contains(l, "[AUTH]") {
		t.Errorf("AUTH with a bad password = %q, want an [AUTH] response code", l)
	}
}

// TestPOP3AuthPlainRefusesHostileTokens proves the RFC 4616 PLAIN token is read
// strictly: a token that is not base64, one missing the password or the authcid,
// one with extra fields, and one asking to act as another user (an authzid that
// is not the authcid) are each refused with [AUTH], and the session stays in the
// AUTHORIZATION state, so a TRANSACTION command is still refused.
func TestPOP3AuthPlainRefusesHostileTokens(t *testing.T) {
	for _, tc := range []struct{ name, token string }{
		{"not base64", "!!!not-base64!!!"},
		{"no password", b64("\x00alice\x00")},
		{"no authcid", b64("\x00\x00secret")},
		{"extra field", b64("\x00alice\x00secret\x00more")},
		{"acting as another user", b64("bob\x00alice\x00secret")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
			send, wantOK, wantERR := c.send, c.wantOK, c.wantERR
			wantOK() // greeting
			send("AUTH PLAIN " + tc.token)
			if l := wantERR(); !strings.Contains(l, "[AUTH]") {
				t.Errorf("AUTH PLAIN %s = %q, want an [AUTH] refusal", tc.name, l)
			}
			send("STAT")
			wantERR() // still unauthenticated
		})
	}
}

// authPlainReply logs in as alice over AUTH PLAIN against a server on auth and
// returns the server's reply.
func authPlainReply(t *testing.T, auth privDir) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)
	auth.StaticAccounts = directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}

	ln := serveOn(t, &Server{Auth: auth, Hostname: "mail.test"})
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	r := textproto.NewReader(bufio.NewReader(conn))
	wantReply(t, r, "+OK") // greeting
	mustSend(t, conn, "AUTH PLAIN %s\r\n", b64("\x00alice\x00secret"))
	return mustReadLine(t, r)
}

// TestPOP3AuthPrivilegeDenied proves the AUTH path hits the same POP3/IMAP
// privilege gate as PASS, a valid credential with the service disabled is refused,
// so SASL is not a gate bypass. A directory that cannot name the account's
// privileges is refused the same way.
func TestPOP3AuthPrivilegeDenied(t *testing.T) {
	for name, auth := range map[string]privDir{
		"service disabled":   {privs: directory.ServicePrivileges{POP3IMAP: false, SMTP: true}},
		"privileges unknown": {unknown: true},
	} {
		if l := authPlainReply(t, auth); !strings.HasPrefix(l, "-ERR") || !strings.Contains(l, "[AUTH]") {
			t.Errorf("%s: AUTH = %q, want -ERR [AUTH]", name, l)
		}
	}
}
