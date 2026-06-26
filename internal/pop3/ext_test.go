package pop3

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// dialPOP3 provisions a single-message mailbox, starts a server, and returns a
// connected textproto reader plus send/read helpers.
func dialPOP3(t *testing.T, msg string) (*textproto.Reader, func(string), func() string, func() string, func() []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(msg), time.Unix(1, 0), 0); err != nil {
		t.Fatal(err)
	}
	st.Close()

	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
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

	send := func(s string) { fmt.Fprintf(conn, "%s\r\n", s) }
	wantOK := func() string {
		t.Helper()
		l, err := r.ReadLine()
		if err != nil || !strings.HasPrefix(l, "+OK") {
			t.Fatalf("want +OK, got %q (err %v)", l, err)
		}
		return l
	}
	wantERR := func() string {
		t.Helper()
		l, err := r.ReadLine()
		if err != nil || !strings.HasPrefix(l, "-ERR") {
			t.Fatalf("want -ERR, got %q (err %v)", l, err)
		}
		return l
	}
	readLines := func() []string {
		t.Helper()
		var lines []string
		for {
			l, err := r.ReadLine()
			if err != nil {
				t.Fatal(err)
			}
			if l == "." {
				return lines
			}
			lines = append(lines, l)
		}
	}
	return r, send, wantOK, wantERR, readLines
}

// TestPOP3CapaAdvertisesExtensions checks the CAPA list advertises every optional
// command and extension hermEX implements, in both protocol states.
func TestPOP3CapaAdvertisesExtensions(t *testing.T) {
	r, send, wantOK, _, readLines := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
	wantOK() // greeting

	send("CAPA")
	wantOK()
	caps := strings.Join(readLines(), " ")
	for _, want := range []string{"TOP", "USER", "UIDL", "PIPELINING", "RESP-CODES", "LOGIN-DELAY 0", "EXPIRE NEVER", "UTF8", "LANG", "IMPLEMENTATION hermEX"} {
		if !strings.Contains(caps, want) {
			t.Errorf("CAPA missing %q; got: %s", want, caps)
		}
	}

	// CAPA must also work in the TRANSACTION state (RFC 2449), not just before auth.
	send("USER alice")
	wantOK()
	send("PASS secret")
	wantOK()
	send("CAPA")
	wantOK()
	if caps := strings.Join(readLines(), " "); !strings.Contains(caps, "UIDL") {
		t.Errorf("CAPA in TRANSACTION state failed; got: %s", caps)
	}
	_ = r
}

// TestPOP3AuthRespCode checks a failed login carries the RFC 3206 [AUTH] code.
func TestPOP3AuthRespCode(t *testing.T) {
	_, send, wantOK, wantERR, _ := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
	wantOK() // greeting
	send("USER alice")
	wantOK()
	send("PASS wrong")
	if l := wantERR(); !strings.Contains(l, "[AUTH]") {
		t.Errorf("auth failure = %q, want an [AUTH] response code", l)
	}
}

// TestPOP3UTF8AndLang checks the RFC 6856 UTF8 and LANG commands.
func TestPOP3UTF8AndLang(t *testing.T) {
	_, send, wantOK, wantERR, readLines := dialPOP3(t, "Subject: x\r\n\r\nbody\r\n")
	wantOK() // greeting

	send("UTF8")
	wantOK()

	send("LANG")
	wantOK()
	if lines := readLines(); len(lines) == 0 || !strings.HasPrefix(lines[0], "en ") {
		t.Errorf("LANG listing = %v, want a line starting with \"en \"", lines)
	}

	send("LANG en")
	if l := wantOK(); !strings.HasPrefix(l, "+OK en ") {
		t.Errorf("LANG en = %q, want \"+OK en ...\"", l)
	}

	send("LANG zz")
	wantERR() // unsupported language
}

// TestPOP3Top checks RFC 1939 TOP returns headers plus the requested body lines.
func TestPOP3Top(t *testing.T) {
	msg := "Subject: top test\r\nFrom: a@b.test\r\n\r\nLINE_ONE\r\nLINE_TWO\r\nLINE_THREE\r\n"
	r, send, wantOK, wantERR, _ := dialPOP3(t, msg)
	wantOK() // greeting
	send("USER alice")
	wantOK()
	send("PASS secret")
	wantOK()

	// TOP 1 0: headers only, no body lines.
	send("TOP 1 0")
	wantOK()
	head, err := io.ReadAll(r.DotReader())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(head), "Subject: top test") {
		t.Errorf("TOP 1 0 missing headers: %q", head)
	}
	if strings.Contains(string(head), "LINE_ONE") {
		t.Errorf("TOP 1 0 leaked a body line: %q", head)
	}

	// TOP 1 99: headers plus the whole (3-line) body.
	send("TOP 1 99")
	wantOK()
	full, err := io.ReadAll(r.DotReader())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"LINE_ONE", "LINE_TWO", "LINE_THREE"} {
		if !strings.Contains(string(full), want) {
			t.Errorf("TOP 1 99 missing body line %q: %q", want, full)
		}
	}

	// Error cases: unknown message and a malformed argument count.
	send("TOP 99 1")
	wantERR()
	send("TOP 1")
	wantERR()
}
