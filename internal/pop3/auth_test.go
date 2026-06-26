package pop3

import (
	"bufio"
	"encoding/base64"
	"fmt"
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
	_, send, wantOK, _, _ := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
	wantOK() // greeting
	send("AUTH PLAIN " + b64("\x00alice\x00secret"))
	wantOK() // authenticated, maildrop opened
	send("STAT")
	wantOK() // a TRANSACTION command now works
}

// TestPOP3AuthPlainContinuation checks AUTH PLAIN without an initial response: the
// server issues a "+ " continuation and reads the base64 credential.
func TestPOP3AuthPlainContinuation(t *testing.T) {
	r, send, wantOK, _, _ := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
	wantOK() // greeting
	send("AUTH PLAIN")
	if l, err := r.ReadLine(); err != nil || !strings.HasPrefix(l, "+ ") {
		t.Fatalf("want a \"+ \" continuation, got %q (err %v)", l, err)
	}
	send(b64("\x00alice\x00secret"))
	wantOK()
}

// TestPOP3AuthLogin checks the AUTH LOGIN username/password challenge exchange.
func TestPOP3AuthLogin(t *testing.T) {
	r, send, wantOK, _, _ := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
	wantOK() // greeting
	send("AUTH LOGIN")
	if l, _ := r.ReadLine(); l != "+ "+b64("Username:") {
		t.Fatalf("want a username challenge, got %q", l)
	}
	send(b64("alice"))
	if l, _ := r.ReadLine(); l != "+ "+b64("Password:") {
		t.Fatalf("want a password challenge, got %q", l)
	}
	send(b64("secret"))
	wantOK()
}

// TestPOP3AuthList checks AUTH with no mechanism lists the supported ones.
func TestPOP3AuthList(t *testing.T) {
	_, send, wantOK, _, readLines := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
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
	_, send, wantOK, wantERR, _ := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
	wantOK() // greeting
	send("AUTH PLAIN " + b64("\x00alice\x00wrong"))
	if l := wantERR(); !strings.Contains(l, "[AUTH]") {
		t.Errorf("AUTH with a bad password = %q, want an [AUTH] response code", l)
	}
}

// TestPOP3AuthPrivilegeDenied proves the AUTH path hits the same POP3/IMAP
// privilege gate as PASS — a valid credential with the service disabled is refused,
// so SASL is not a gate bypass.
func TestPOP3AuthPrivilegeDenied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	auth := privDir{
		StaticAccounts: directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}},
		privs:          directory.ServicePrivileges{POP3IMAP: false, SMTP: true},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go (&Server{Auth: auth, Hostname: "mail.test"}).Serve(ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	r := textproto.NewReader(bufio.NewReader(conn))
	if l, _ := r.ReadLine(); !strings.HasPrefix(l, "+OK") { // greeting
		t.Fatalf("greeting = %q", l)
	}
	fmt.Fprintf(conn, "AUTH PLAIN %s\r\n", b64("\x00alice\x00secret"))
	l, err := r.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(l, "-ERR") || !strings.Contains(l, "[AUTH]") {
		t.Errorf("AUTH with POP3/IMAP disabled = %q, want -ERR [AUTH]", l)
	}
}
