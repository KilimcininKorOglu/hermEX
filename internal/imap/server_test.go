package imap

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// testClient drives the IMAP server over a real TCP connection.
type testClient struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
}

func startServer(t *testing.T) (*testClient, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inbox := int64(mapi.PrivateFIDInbox)
	// Two messages: the first unseen, the second \Seen.
	if _, err := st.AppendMessage(inbox, []byte("Subject: one\r\n\r\nbody"), time.Unix(1, 0), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(inbox, []byte("Subject: two\r\n\r\nbody"), time.Unix(2, 0), objectstore.FlagSeen); err != nil {
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
	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")
	return c, path
}

// line reads one logical response line, inlining any IMAP literals ({n}CRLF +
// n bytes) so a multi-line literal body comes back as a single string.
func (c *testClient) line() string {
	c.t.Helper()
	s, err := c.br.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	line := strings.TrimRight(s, "\r\n")
	for {
		n, ok := literalSuffix(line)
		if !ok {
			return line
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.br, buf); err != nil {
			c.t.Fatalf("read literal: %v", err)
		}
		more, err := c.br.ReadString('\n')
		if err != nil {
			c.t.Fatalf("read: %v", err)
		}
		line += string(buf) + strings.TrimRight(more, "\r\n")
	}
}

// literalSuffix reports whether line ends with a "{n}" literal marker and, if
// so, returns n.
func literalSuffix(line string) (int, bool) {
	if !strings.HasSuffix(line, "}") {
		return 0, false
	}
	i := strings.LastIndex(line, "{")
	if i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(line[i+1 : len(line)-1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// expectUntagged reads one untagged line and asserts it begins with "* <word>".
func (c *testClient) expectUntagged(word, what string) string {
	c.t.Helper()
	l := c.line()
	if !strings.HasPrefix(l, "* "+word) {
		c.t.Fatalf("%s: got %q, want untagged %s", what, l, word)
	}
	return l
}

// do sends a tagged command and returns the untagged lines plus the tagged
// status word (OK/NO/BAD).
func (c *testClient) do(tag, cmd string) (untagged []string, status string) {
	c.t.Helper()
	fmt.Fprintf(c.conn, "%s %s\r\n", tag, cmd)
	return c.collect(tag)
}

// collect reads untagged responses until the tagged completion for tag,
// returning the untagged lines and the tagged status word.
func (c *testClient) collect(tag string) (untagged []string, status string) {
	c.t.Helper()
	for {
		l := c.line()
		if strings.HasPrefix(l, tag+" ") {
			return untagged, strings.Fields(l)[1]
		}
		untagged = append(untagged, l)
	}
}

// appendMsg performs an APPEND with a synchronizing literal, exercising the
// continuation handshake, and returns the tagged status.
func (c *testClient) appendMsg(tag, mailbox, msg string) string {
	c.t.Helper()
	fmt.Fprintf(c.conn, "%s APPEND %s {%d}\r\n", tag, mailbox, len(msg))
	if cont := c.line(); !strings.HasPrefix(cont, "+") {
		c.t.Fatalf("APPEND continuation = %q, want +", cont)
	}
	fmt.Fprintf(c.conn, "%s\r\n", msg)
	_, status := c.collect(tag)
	return status
}

func (c *testClient) mustOK(tag, cmd string) []string {
	c.t.Helper()
	un, status := c.do(tag, cmd)
	if status != "OK" {
		c.t.Fatalf("%s: status %s, want OK", cmd, status)
	}
	return un
}

func TestIMAPLoginSelectListStatus(t *testing.T) {
	c, _ := startServer(t)

	if un := c.mustOK("a1", "CAPABILITY"); !hasPrefixAny(un, "* CAPABILITY IMAP4rev1") {
		t.Errorf("CAPABILITY untagged = %v", un)
	}

	if _, status := c.do("a2", "LOGIN alice wrong"); status != "NO" {
		t.Errorf("bad LOGIN status = %s, want NO", status)
	}
	c.mustOK("a3", "LOGIN alice secret")

	un := c.mustOK("a4", "SELECT INBOX")
	if !hasPrefixAny(un, "* 2 EXISTS") {
		t.Errorf("SELECT missing '2 EXISTS': %v", un)
	}
	if !slices.Contains(un, "* OK [UNSEEN 1] first unseen") {
		t.Errorf("SELECT missing UNSEEN 1: %v", un)
	}

	un = c.mustOK("a5", `LIST "" "*"`)
	if !containsSubstr(un, `"/" "INBOX"`) {
		t.Errorf("LIST missing INBOX: %v", un)
	}

	un = c.mustOK("a6", "STATUS INBOX (MESSAGES UNSEEN UIDNEXT)")
	if !containsSubstr(un, `STATUS "INBOX" (MESSAGES 2 UNSEEN 1 UIDNEXT 3)`) {
		t.Errorf("STATUS = %v", un)
	}

	_, status := c.do("a7", "LOGOUT")
	if status != "OK" {
		t.Errorf("LOGOUT status = %s", status)
	}
}

func TestIMAPCreateSubscribeLsub(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")

	c.mustOK("a2", "CREATE Archive")
	if un := c.mustOK("a3", `LIST "" "*"`); !containsSubstr(un, `"Archive"`) {
		t.Errorf("LIST after CREATE missing Archive: %v", un)
	}
	// Created folders are subscribed by default, so LSUB shows Archive...
	if un := c.mustOK("a4", `LSUB "" "*"`); !containsSubstr(un, `"Archive"`) {
		t.Errorf("LSUB missing Archive: %v", un)
	}
	// ...and disappears from LSUB after UNSUBSCRIBE, while staying in LIST.
	c.mustOK("a5", "UNSUBSCRIBE Archive")
	if un := c.mustOK("a6", `LSUB "" "*"`); containsSubstr(un, `"Archive"`) {
		t.Errorf("LSUB still lists unsubscribed Archive: %v", un)
	}
	if un := c.mustOK("a7", `LIST "" "*"`); !containsSubstr(un, `"Archive"`) {
		t.Errorf("LIST dropped Archive after UNSUBSCRIBE: %v", un)
	}

	// DELETE removes it from LIST.
	c.mustOK("a8", "DELETE Archive")
	if un := c.mustOK("a9", `LIST "" "*"`); containsSubstr(un, `"Archive"`) {
		t.Errorf("LIST still has deleted Archive: %v", un)
	}
}

func TestIMAPAuthenticatePlain(t *testing.T) {
	c, _ := startServer(t)
	// SASL PLAIN initial response: base64( authzid NUL authcid NUL passwd ).
	ir := base64.StdEncoding.EncodeToString([]byte("\x00alice\x00secret"))
	c.mustOK("a1", "AUTHENTICATE PLAIN "+ir)
	c.mustOK("a2", "SELECT INBOX")
}

func TestIMAPFetch(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX")

	un := c.mustOK("a3", "FETCH 1 (FLAGS RFC822.SIZE ENVELOPE)")
	if !containsSubstr(un, `"one"`) || !containsSubstr(un, "RFC822.SIZE") || !containsSubstr(un, "ENVELOPE (") {
		t.Errorf("FETCH envelope = %v", un)
	}

	if un := c.mustOK("a4", "FETCH 1 BODYSTRUCTURE"); !containsSubstr(un, `("TEXT" "PLAIN"`) {
		t.Errorf("FETCH BODYSTRUCTURE = %v", un)
	}

	// BODY.PEEK echoes BODY[...] and returns the header bytes via a literal.
	if un := c.mustOK("a5", "FETCH 1 BODY.PEEK[HEADER]"); !containsSubstr(un, "Subject: one") {
		t.Errorf("FETCH BODY.PEEK[HEADER] = %v", un)
	}

	// UID FETCH names UIDs and always includes the UID in the response.
	un = c.mustOK("a6", "UID FETCH 2 (FLAGS)")
	if !containsSubstr(un, "UID 2") || !hasPrefixAny(un, "* 2 FETCH") {
		t.Errorf("UID FETCH = %v", un)
	}
}

func TestIMAPFetchSeenSemantics(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX")

	// PEEK must not set \Seen on the still-unseen message 1.
	c.mustOK("a3", "FETCH 1 BODY.PEEK[]")
	if un := c.mustOK("a4", "FETCH 1 FLAGS"); containsSubstr(un, `\Seen`) {
		t.Errorf("PEEK set \\Seen: %v", un)
	}

	// A non-peek body fetch sets \Seen and reports the new FLAGS in the same
	// response.
	un := c.mustOK("a5", "FETCH 1 BODY[]")
	if !containsSubstr(un, `\Seen`) {
		t.Errorf("BODY[] did not report \\Seen: %v", un)
	}
	if un := c.mustOK("a6", "FETCH 1 FLAGS"); !containsSubstr(un, `\Seen`) {
		t.Errorf("\\Seen not persisted: %v", un)
	}
}

func TestIMAPStoreAndSearch(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX")

	// STORE reports the new flags as an untagged FETCH.
	if un := c.mustOK("a3", `STORE 1 +FLAGS (\Flagged)`); !containsSubstr(un, `\Flagged`) {
		t.Errorf("STORE +FLAGS = %v", un)
	}
	// .SILENT suppresses the FETCH echo.
	if un := c.mustOK("a4", `STORE 1 +FLAGS.SILENT (\Answered)`); containsSubstr(un, "FETCH") {
		t.Errorf("STORE .SILENT should not echo a FETCH: %v", un)
	}

	// SEARCH by flag, by header substring, and by UID.
	if un := c.mustOK("a5", "SEARCH FLAGGED"); !containsSubstr(un, "* SEARCH 1") {
		t.Errorf("SEARCH FLAGGED = %v", un)
	}
	if un := c.mustOK("a6", "SEARCH UNSEEN"); !containsSubstr(un, "* SEARCH 1") {
		t.Errorf("SEARCH UNSEEN = %v", un)
	}
	if un := c.mustOK("a7", `SEARCH SUBJECT "two"`); !containsSubstr(un, "* SEARCH 2") {
		t.Errorf(`SEARCH SUBJECT "two" = %v`, un)
	}
	if un := c.mustOK("a8", "UID SEARCH SEEN"); !containsSubstr(un, "* SEARCH 2") {
		t.Errorf("UID SEARCH SEEN = %v", un)
	}
	// NOT and OR combine.
	if un := c.mustOK("a9", "SEARCH NOT SEEN"); !containsSubstr(un, "* SEARCH 1") {
		t.Errorf("SEARCH NOT SEEN = %v", un)
	}
	// An empty HEADER needle matches messages that HAVE the field (both have
	// Subject), and never matches when the field is absent (no Message-Id).
	if un := c.mustOK("a10", `SEARCH HEADER Subject ""`); !containsSubstr(un, "* SEARCH 1 2") {
		t.Errorf(`SEARCH HEADER Subject "" = %v`, un)
	}
	if un := c.mustOK("a11", `SEARCH HEADER Message-Id ""`); !slices.Contains(un, "* SEARCH") {
		t.Errorf(`SEARCH HEADER Message-Id "" = %v, want empty result`, un)
	}
}

func TestIMAPCopyAndAppend(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "CREATE Archive")
	c.mustOK("a3", "SELECT INBOX")

	// COPY message 1 into Archive, then verify Archive holds exactly one.
	c.mustOK("a4", "COPY 1 Archive")
	if un := c.mustOK("a5", "EXAMINE Archive"); !hasPrefixAny(un, "* 1 EXISTS") {
		t.Errorf("Archive after COPY = %v", un)
	}

	// APPEND a new message into INBOX (synchronizing literal), then confirm the
	// selected mailbox count grows from 2 to 3.
	c.mustOK("a6", "SELECT INBOX")
	if status := c.appendMsg("a7", "INBOX", "Subject: appended\r\n\r\nhi"); status != "OK" {
		t.Fatalf("APPEND status = %s", status)
	}
	if un := c.mustOK("a8", "STATUS INBOX (MESSAGES)"); !containsSubstr(un, "MESSAGES 3") {
		t.Errorf("STATUS after APPEND = %v", un)
	}
}

func TestIMAPExpungeAndClose(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX")

	// Mark message 1 deleted, then EXPUNGE removes it and renumbers.
	c.mustOK("a3", `STORE 1 +FLAGS (\Deleted)`)
	if un := c.mustOK("a4", "EXPUNGE"); !containsSubstr(un, "* 1 EXPUNGE") {
		t.Errorf("EXPUNGE = %v", un)
	}
	if un := c.mustOK("a5", "STATUS INBOX (MESSAGES)"); !containsSubstr(un, "MESSAGES 1") {
		t.Errorf("STATUS after EXPUNGE = %v", un)
	}

	// CLOSE expunges silently (no untagged EXPUNGE) and deselects.
	c.mustOK("a6", `STORE 1 +FLAGS (\Deleted)`)
	if un, status := c.do("a7", "CLOSE"); status != "OK" || containsSubstr(un, "EXPUNGE") {
		t.Errorf("CLOSE un=%v status=%s", un, status)
	}
	// After CLOSE no mailbox is selected, so FETCH is rejected.
	if _, status := c.do("a8", "FETCH 1 FLAGS"); status != "NO" {
		t.Errorf("FETCH after CLOSE status = %s, want NO", status)
	}
}

func hasPrefixAny(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func containsSubstr(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
