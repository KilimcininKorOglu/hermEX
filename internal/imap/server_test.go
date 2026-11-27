package imap

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
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

// testServer pairs a dialed testClient with the backing mailbox path.
type testServer struct {
	client *testClient
	path   string
}

// cmdReply holds one IMAP command's untagged lines, status word, and full tagged line.
type cmdReply struct {
	untagged []string
	status   string
	tagged   string
}

func startServer(t *testing.T) testServer {
	t.Helper()
	path := seededMailbox(t)
	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	return testServer{client: dialServer(t, &Server{Auth: auth, Hostname: "mail.test"}), path: path}
}

// startOrgServer serves alice@hermex.test with the directory behind the
// organization checks: bob is a colleague, mallory an account of another
// organization (static accounts scope by domain).
func startOrgServer(t *testing.T) testServer {
	t.Helper()
	path := seededMailbox(t)
	auth := directory.StaticAccounts{
		"alice@hermex.test":      {Password: "secret", MailboxPath: path},
		"bob@hermex.test":        {Password: "secret", MailboxPath: t.TempDir()},
		"mallory@other-org.test": {Password: "secret", MailboxPath: t.TempDir()},
	}
	return testServer{client: dialServer(t, &Server{Auth: auth, Accounts: auth, Hostname: "mail.test"}), path: path}
}

// seededMailbox creates a mailbox holding two Inbox messages, the first unseen
// and the second \Seen.
func seededMailbox(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inbox := int64(mapi.PrivateFIDInbox)
	// Two messages: the first unseen, the second \Seen.
	mustAppend(t, st, inbox, "Subject: one\r\n\r\nbody", time.Unix(1, 0), 0)
	mustAppend(t, st, inbox, "Subject: two\r\n\r\nbody", time.Unix(2, 0), objectstore.FlagSeen)
	mustClose(t, st)
	return path
}

// dialServer serves srv on a loopback listener and returns a client past the
// greeting.
func dialServer(t *testing.T, srv *Server) *testClient {
	t.Helper()
	ln := serveOn(t, srv)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustClose(t, conn) })
	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")
	return c
}

// TestLoginRefusesAnUnprovisionedMailbox proves a read path never creates a
// mailbox: an account's mailbox is made when the account is created, so a
// missing one (a deleted directory, an unmounted volume) is refused at LOGIN and
// stays missing, rather than being replaced by an empty mailbox that hides the
// loss from the user and the operator.
func TestLoginRefusesAnUnprovisionedMailbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gone")
	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	c := dialServer(t, &Server{Auth: auth, Hostname: "mail.test"})

	if r := c.do("a1", "LOGIN alice secret"); r.status != "NO" {
		t.Errorf("LOGIN to a missing mailbox = %q, want NO", r.status)
	}
	if info, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("LOGIN created the missing mailbox (info=%v, err=%v)", info, err)
	}
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
		n, err := io.ReadFull(c.br, buf)
		if err != nil || n != len(buf) {
			c.t.Fatalf("read literal (%d/%d): %v", n, len(buf), err)
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
// status word and full tagged line.
func (c *testClient) do(tag, cmd string) cmdReply {
	c.t.Helper()
	mustSend(c.t, c.conn, "%s %s\r\n", tag, cmd)
	return c.collect(tag)
}

// mustSend writes a formatted protocol line to w and fails the test when the
// write fails, so a dead connection is reported instead of a confusing read
// timeout further on.
func mustSend(t *testing.T, w io.Writer, format string, args ...any) {
	t.Helper()
	if n, err := fmt.Fprintf(w, format, args...); err != nil || n == 0 {
		t.Fatalf("send %q (n=%d): %v", format, n, err)
	}
}

// collect reads untagged responses until the tagged completion for tag,
// returning the untagged lines, status word, and full tagged line.
func (c *testClient) collect(tag string) cmdReply {
	c.t.Helper()
	var untagged []string
	for {
		l := c.line()
		if strings.HasPrefix(l, tag+" ") {
			return cmdReply{untagged: untagged, status: strings.Fields(l)[1], tagged: l}
		}
		untagged = append(untagged, l)
	}
}

// appendMsg performs an APPEND with a synchronizing literal, exercising the
// continuation handshake, and returns the tagged status.
func (c *testClient) appendMsg(tag, mailbox, msg string) string {
	c.t.Helper()
	mustSend(c.t, c.conn, "%s APPEND %s {%d}\r\n", tag, mailbox, len(msg))
	if cont := c.line(); !strings.HasPrefix(cont, "+") {
		c.t.Fatalf("APPEND continuation = %q, want +", cont)
	}
	mustSend(c.t, c.conn, "%s\r\n", msg)
	return c.collect(tag).status
}

func (c *testClient) mustOK(tag, cmd string) []string {
	c.t.Helper()
	r := c.do(tag, cmd)
	if r.status != "OK" {
		c.t.Fatalf("%s: status %s, want OK", cmd, r.status)
	}
	return r.untagged
}

func TestIMAPLoginSelectListStatus(t *testing.T) {
	c := startServer(t).client

	if un := c.mustOK("a1", "CAPABILITY"); !hasPrefixAny(un, "* CAPABILITY IMAP4rev1") {
		t.Errorf("CAPABILITY untagged = %v", un)
	}

	if r := c.do("a2", "LOGIN alice wrong"); r.status != "NO" {
		t.Errorf("bad LOGIN status = %s, want NO", r.status)
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

	r := c.do("a7", "LOGOUT")
	if r.status != "OK" {
		t.Errorf("LOGOUT status = %s, untagged=%v", r.status, r.untagged)
	}
}

func TestIMAPCreateSubscribeLsub(t *testing.T) {
	c := startServer(t).client
	c.mustOK("a1", "LOGIN alice secret")

	c.mustOK("a2", "CREATE Projects")
	if un := c.mustOK("a3", `LIST "" "*"`); !containsSubstr(un, `"Projects"`) {
		t.Errorf("LIST after CREATE missing Projects: %v", un)
	}
	// Created folders are subscribed by default, so LSUB shows Projects...
	if un := c.mustOK("a4", `LSUB "" "*"`); !containsSubstr(un, `"Projects"`) {
		t.Errorf("LSUB missing Projects: %v", un)
	}
	// ...and disappears from LSUB after UNSUBSCRIBE, while staying in LIST.
	c.mustOK("a5", "UNSUBSCRIBE Projects")
	if un := c.mustOK("a6", `LSUB "" "*"`); containsSubstr(un, `"Projects"`) {
		t.Errorf("LSUB still lists unsubscribed Projects: %v", un)
	}
	if un := c.mustOK("a7", `LIST "" "*"`); !containsSubstr(un, `"Projects"`) {
		t.Errorf("LIST dropped Projects after UNSUBSCRIBE: %v", un)
	}

	// DELETE removes it from LIST.
	c.mustOK("a8", "DELETE Projects")
	if un := c.mustOK("a9", `LIST "" "*"`); containsSubstr(un, `"Projects"`) {
		t.Errorf("LIST still has deleted Projects: %v", un)
	}

	// Deleting INBOX is refused and leaves INBOX in LIST (RFC 3501 6.3.4).
	if r := c.do("a10", "DELETE INBOX"); r.status != "NO" {
		t.Errorf("DELETE INBOX = %s, want NO", r.status)
	}
	if un := c.mustOK("a11", `LIST "" "*"`); !containsSubstr(un, `"INBOX"`) {
		t.Errorf("INBOX missing from LIST after refused DELETE: %v", un)
	}
}

func TestIMAPAuthenticatePlain(t *testing.T) {
	c := startServer(t).client
	// SASL PLAIN initial response: base64( authzid NUL authcid NUL passwd ).
	ir := base64.StdEncoding.EncodeToString([]byte("\x00alice\x00secret"))
	c.mustOK("a1", "AUTHENTICATE PLAIN "+ir)
	c.mustOK("a2", "SELECT INBOX")
}

// TestAuthenticatePlainRefusesAnotherAuthorizationIdentity sends alice's valid
// credentials asking to act as bob. RFC 4616 section 2: the server verifies that
// the authentication identity may act as the authorization identity, and the
// authentication fails if not. This server grants no proxy rights, so only an
// empty authzid or one equal to the authcid may log in.
func TestAuthenticatePlainRefusesAnotherAuthorizationIdentity(t *testing.T) {
	c := startServer(t).client
	ir := base64.StdEncoding.EncodeToString([]byte("bob\x00alice\x00secret"))
	if r := c.do("a1", "AUTHENTICATE PLAIN "+ir); r.status == "OK" {
		t.Fatal("alice's credentials logged in while asking to act as bob")
	}
	if r := c.do("a1s", "SELECT INBOX"); r.status != "NO" {
		t.Errorf("SELECT after refused AUTHENTICATE = %s, want NO", r.status)
	}
	ir = base64.StdEncoding.EncodeToString([]byte("alice\x00alice\x00secret"))
	c.mustOK("a2", "AUTHENTICATE PLAIN "+ir)
}

func TestIMAPFetch(t *testing.T) {
	c := startServer(t).client
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
	c := startServer(t).client
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
	c := startServer(t).client
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
	// RFC 3501 6.4.4: KEYWORD and UNKEYWORD take a flag-keyword, so the bare key
	// is a syntax error and the command is answered BAD.
	wantStatus(t, "SEARCH KEYWORD without its argument", c, "a6k", "SEARCH KEYWORD", "BAD")
	wantStatus(t, "SEARCH UNKEYWORD before a list", c, "a6u", "SEARCH UNKEYWORD (SEEN)", "BAD")
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
	c := startServer(t).client
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "CREATE Projects")
	c.mustOK("a3", "SELECT INBOX")

	// COPY message 1 into Projects, then verify Projects holds exactly one.
	c.mustOK("a4", "COPY 1 Projects")
	if un := c.mustOK("a5", "EXAMINE Projects"); !hasPrefixAny(un, "* 1 EXISTS") {
		t.Errorf("Projects after COPY = %v", un)
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
	c := startServer(t).client
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
	if r := c.do("a7", "CLOSE"); r.status != "OK" || containsSubstr(r.untagged, "EXPUNGE") {
		t.Errorf("CLOSE un=%v status=%s", r.untagged, r.status)
	}
	// After CLOSE no mailbox is selected, so FETCH is rejected.
	if r := c.do("a8", "FETCH 1 FLAGS"); r.status != "NO" {
		t.Errorf("FETCH after CLOSE status = %s, want NO", r.status)
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
