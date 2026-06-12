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
	"hermex/internal/store"
)

func TestPOP3RetrieveAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice.sqlite3")

	// Provision a mailbox with two messages; msgB has a line starting with '.'
	// to exercise dot-stuffing on the wire.
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := st.CreateFolder(nil, inboxName)
	if err != nil {
		t.Fatal(err)
	}
	msgA := "Subject: one\r\n\r\nbody one\r\n"
	msgB := "Subject: two\r\n\r\n.dotted line\r\nmore\r\n"
	if _, err := st.AppendMessage(inbox, []byte(msgA), time.Unix(1, 0), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(inbox, []byte(msgB), time.Unix(2, 0), 0); err != nil {
		t.Fatal(err)
	}
	st.Close()

	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go (&Server{Auth: auth, Hostname: "mail.test"}).Serve(ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := textproto.NewReader(bufio.NewReader(conn))

	send := func(s string) { fmt.Fprintf(conn, "%s\r\n", s) }
	wantOK := func() string {
		t.Helper()
		l, err := r.ReadLine()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(l, "+OK") {
			t.Fatalf("want +OK, got %q", l)
		}
		return l
	}
	wantERR := func() {
		t.Helper()
		l, err := r.ReadLine()
		if err != nil || !strings.HasPrefix(l, "-ERR") {
			t.Fatalf("want -ERR, got %q (err %v)", l, err)
		}
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

	wantOK() // greeting
	send("USER alice")
	wantOK()
	send("PASS wrong")
	wantERR() // bad password
	send("USER alice")
	wantOK()
	send("PASS secret")
	wantOK()

	send("STAT")
	if l := wantOK(); !strings.HasPrefix(l, "+OK 2 ") {
		t.Errorf("STAT = %q, want +OK 2 <size>", l)
	}

	send("LIST")
	wantOK()
	if lines := readLines(); len(lines) != 2 || !strings.HasPrefix(lines[0], "1 ") || !strings.HasPrefix(lines[1], "2 ") {
		t.Errorf("LIST lines = %v", lines)
	}

	send("UIDL")
	wantOK()
	if lines := readLines(); len(lines) != 2 || lines[0] != "1 1" || lines[1] != "2 2" {
		t.Errorf("UIDL lines = %v, want [1 1 2 2]", lines)
	}

	// RETR 2 returns msgB; the wire form is dot-stuffed, so decode it with a
	// DotReader (which also normalizes CRLF to LF) and compare LF-normalized.
	send("RETR 2")
	wantOK()
	body, err := io.ReadAll(r.DotReader())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(body), strings.ReplaceAll(msgB, "\r\n", "\n"); got != want {
		t.Errorf("RETR body = %q, want %q", got, want)
	}

	send("DELE 1")
	wantOK()
	send("RETR 1")
	wantERR() // deleted message is no longer retrievable
	send("QUIT")
	wantOK()

	// The deletion must be committed: message 1 (UID 1) is gone, UID 2 remains.
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	msgs, err := st2.ListMessages(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].UID != 2 {
		t.Errorf("after QUIT, messages = %+v, want only UID 2", msgs)
	}
}
