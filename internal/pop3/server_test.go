package pop3

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// mustSend writes a formatted protocol line to w and fails the test when the
// write fails, so a dead connection is reported instead of a confusing read
// failure further on.
func mustSend(t *testing.T, w io.Writer, format string, args ...any) {
	t.Helper()
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		t.Fatalf("send %q: %v", format, err)
	}
}

// TestLoginRefusesAnUnprovisionedMailbox proves a read path never creates a
// mailbox: an account's mailbox is made when the account is created, so a
// missing one is refused at PASS and stays missing, rather than being replaced
// by an empty maildrop that hides the loss.
func TestLoginRefusesAnUnprovisionedMailbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gone")
	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	ln := serveOn(t, &Server{Auth: auth, Hostname: "mail.test"})
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	r := textproto.NewReader(bufio.NewReader(conn))
	wantReply(t, r, "+OK")
	mustSend(t, conn, "USER alice\r\n")
	wantReply(t, r, "+OK")
	mustSend(t, conn, "PASS secret\r\n")
	wantReply(t, r, "-ERR")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("PASS created the missing mailbox: %v", err)
	}
}

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
	mustClose(t, st)

	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	ln := serveOn(t, &Server{Auth: auth, Hostname: "mail.test"})
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	r := textproto.NewReader(bufio.NewReader(conn))

	send := func(s string) { mustSend(t, conn, "%s\r\n", s) }
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

	// RFC 1939: a msg argument names an existing, undeleted message (1-based); any
	// other number, or none, is answered -ERR and changes nothing.
	for _, cmd := range []string{"RETR 0", "RETR -1", "RETR 3", "RETR 99999999999999999999", "RETR abc",
		"DELE 0", "DELE 3", "DELE x", "LIST 0", "LIST 3"} {
		send(cmd)
		wantERR()
	}
	send("STAT")
	wantLinePrefix(t, wantOK(), "+OK 2 ", "STAT after the refused commands")

	send("DELE 1")
	wantOK()
	send("DELE 1")
	wantERR() // already marked deleted (RFC 1939 DELE)
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
	defer mustClose(t, st)
	msgs, err := st.ListMessages(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("after QUIT, messages = %+v, want only UID 2", msgs)
	}
	wantEq(t, msgs[0].UID, uint32(2), "the surviving message's uid")
	dump, err := st.ListSoftDeleted(inbox)
	if err != nil {
		t.Fatal(err)
	}
	wantEq(t, len(dump), 1, "dumpster items after POP3 DELE+QUIT (recoverable)")
}
