package pop3

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

func TestPOP3RetrieveAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")

	// Provision a mailbox with two messages; msgB has a line starting with '.'
	// to exercise dot-stuffing on the wire.
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inbox := int64(mapi.PrivateFIDInbox)
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
	go func() { _ = (&Server{Auth: auth, Hostname: "mail.test"}).Serve(ln) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := textproto.NewReader(bufio.NewReader(conn))

	send := func(s string) { _, _ = fmt.Fprintf(conn, "%s\r\n", s) }
	wantOK := func() string {
		t.Helper()
		return wantReply(t, r, "+OK")
	}
	wantERR := func() {
		t.Helper()
		wantReply(t, r, "-ERR")
	}
	readLines := func() []string {
		t.Helper()
		return readMultiline(t, r)
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
	wantLinePrefix(t, wantOK(), "+OK 2 ", "STAT")

	send("LIST")
	wantOK()
	lines := readLines()
	if len(lines) != 2 {
		t.Fatalf("LIST lines = %v, want two", lines)
	}
	wantLinePrefix(t, lines[0], "1 ", "the first LIST line")
	wantLinePrefix(t, lines[1], "2 ", "the second LIST line")

	send("UIDL")
	wantOK()
	lines = readLines()
	if len(lines) != 2 {
		t.Fatalf("UIDL lines = %v, want two", lines)
	}
	wantEq(t, lines[0], "1 1", "the first UIDL line")
	wantEq(t, lines[1], "2 2", "the second UIDL line")

	// RETR 2 returns msgB re-synthesized from the stored object (not byte
	// identical to arrival). Its body line starting with '.' is dot-stuffed on
	// the wire; decode with a DotReader and assert the subject and the
	// dot-prefixed line survived the round trip and de-stuffing.
	send("RETR 2")
	wantOK()
	body, err := io.ReadAll(r.DotReader())
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, string(body), "Subject: two", "the retrieved body")
	wantContains(t, string(body), ".dotted line", "the retrieved body after de-stuffing")

	send("DELE 1")
	wantOK()
	send("RETR 1")
	wantERR() // deleted message is no longer retrievable
	send("QUIT")
	wantOK()

	checkDeletionCommitted(t, path, inbox)
}

// checkDeletionCommitted asserts the QUIT commit landed: message 1 (UID 1) is
// gone and UID 2 remains, and the deleted message went to the Recoverable Items
// dumpster rather than being purged.
func checkDeletionCommitted(t *testing.T, path string, inbox int64) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("after QUIT, messages = %+v, want only UID 2", msgs)
	}
	wantEq(t, msgs[0].UID, uint32(2), "the surviving message's uid")
	dump, _ := st.ListSoftDeleted(inbox)
	wantEq(t, len(dump), 1, "dumpster items after POP3 DELE+QUIT (recoverable)")
}
