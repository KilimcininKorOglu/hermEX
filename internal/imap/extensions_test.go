package imap

import (
	"bufio"
	"compress/flate"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"hermex/internal/objectstore"
)

// collectTagged reads responses until the tagged completion for tag and returns
// that full line (so a test can inspect a [RESPONSE-CODE] in it).
func (c *testClient) collectTagged(tag string) string {
	c.t.Helper()
	for {
		l := c.line()
		if strings.HasPrefix(l, tag+" ") {
			return l
		}
	}
}

// taggedLine sends a command and returns its full tagged completion line.
func (c *testClient) taggedLine(tag, cmd string) string {
	c.t.Helper()
	fmt.Fprintf(c.conn, "%s %s\r\n", tag, cmd)
	return c.collectTagged(tag)
}

// TestIMAPIDUnselectCapability covers the cheap RFC additions: the CHILDREN/ID/
// UNSELECT capabilities, ID returning the server name in any state, and UNSELECT
// returning to the authenticated state WITHOUT expunging \Deleted messages.
func TestIMAPIDUnselectCapability(t *testing.T) {
	c, _ := startServer(t)

	caps := strings.Join(c.mustOK("a1", "CAPABILITY"), " ")
	for _, want := range []string{"CHILDREN", "ID", "UNSELECT"} {
		if !strings.Contains(caps, want) {
			t.Errorf("CAPABILITY missing %q: %s", want, caps)
		}
	}

	// ID is valid in any state (here before login) and returns the server name.
	un, status := c.do("a2", `ID ("name" "TestClient")`)
	if status != "OK" {
		t.Fatalf("ID status = %s, want OK", status)
	}
	if !strings.Contains(strings.Join(un, " "), `"name" "hermEX"`) {
		t.Errorf("ID response = %v, want the server name", un)
	}

	// UNSELECT needs a selected mailbox.
	if _, status := c.do("a3", "UNSELECT"); status != "NO" {
		t.Errorf("UNSELECT with no mailbox = %s, want NO", status)
	}

	// Mark a message \Deleted, then UNSELECT: it must NOT be expunged, so a
	// re-SELECT still reports both messages (CLOSE would have expunged it).
	c.mustOK("a4", "LOGIN alice secret")
	c.mustOK("a5", "SELECT INBOX")
	c.mustOK("a6", `STORE 1 +FLAGS (\Deleted)`)
	c.mustOK("a7", "UNSELECT")
	reselect := strings.Join(c.mustOK("a8", "SELECT INBOX"), " ")
	if !strings.Contains(reselect, "2 EXISTS") {
		t.Errorf("after UNSELECT, SELECT = %q, want 2 EXISTS (no expunge)", reselect)
	}
}

// TestIMAPUIDPlus covers UIDPLUS (RFC 4315): APPENDUID on APPEND, COPYUID on COPY,
// and selective UID EXPUNGE.
func TestIMAPUIDPlus(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX") // two messages, UIDs 1 and 2

	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "UIDPLUS") {
		t.Errorf("CAPABILITY missing UIDPLUS: %s", caps)
	}

	// APPEND replies with an [APPENDUID uidvalidity uid] response code.
	msg := "Subject: appended\r\n\r\nbody"
	fmt.Fprintf(c.conn, "a3 APPEND INBOX {%d}\r\n", len(msg))
	if cont := c.line(); !strings.HasPrefix(cont, "+") {
		t.Fatalf("APPEND continuation = %q, want +", cont)
	}
	fmt.Fprintf(c.conn, "%s\r\n", msg)
	if a3 := c.collectTagged("a3"); !strings.Contains(a3, "[APPENDUID ") {
		t.Errorf("APPEND = %q, want an [APPENDUID ...] response code", a3)
	}

	// COPY into a new folder replies with a [COPYUID uidvalidity src dst] code.
	c.mustOK("a4", "CREATE Archive")
	if a5 := c.taggedLine("a5", "COPY 1 Archive"); !strings.Contains(a5, "[COPYUID ") {
		t.Errorf("COPY = %q, want a [COPYUID ...] response code", a5)
	}

	// UID EXPUNGE removes only the targeted \Deleted UID; the other \Deleted
	// message (UID 2, outside the set) survives.
	c.mustOK("a6", `STORE 1:2 +FLAGS (\Deleted)`)
	un, status := c.do("a7", "UID EXPUNGE 1")
	if status != "OK" {
		t.Fatalf("UID EXPUNGE status = %s, want OK", status)
	}
	exp := 0
	for _, l := range un {
		if strings.HasSuffix(l, "EXPUNGE") {
			exp++
		}
	}
	if exp != 1 {
		t.Errorf("UID EXPUNGE 1 emitted %d EXPUNGE lines, want exactly 1: %v", exp, un)
	}
	if got := strings.Join(c.mustOK("a8", "UID FETCH 2 (UID)"), " "); !strings.Contains(got, "UID 2") {
		t.Errorf("UID 2 (\\Deleted but outside the set) should survive UID EXPUNGE 1: %q", got)
	}
}

// TestIMAPMove covers MOVE (RFC 6851): the message leaves the source (COPYUID +
// EXPUNGE) and lands in the destination.
func TestIMAPMove(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX") // UIDs 1, 2
	c.mustOK("a3", "CREATE Archive")

	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "MOVE") {
		t.Errorf("CAPABILITY missing MOVE: %s", caps)
	}

	un, status := c.do("a4", "MOVE 1 Archive")
	if status != "OK" {
		t.Fatalf("MOVE status = %s, want OK", status)
	}
	joined := strings.Join(un, " ")
	if !strings.Contains(joined, "[COPYUID ") {
		t.Errorf("MOVE missing the [COPYUID ...] untagged OK: %v", un)
	}
	if !strings.Contains(joined, "EXPUNGE") {
		t.Errorf("MOVE missing the untagged EXPUNGE: %v", un)
	}

	if got := strings.Join(c.mustOK("a5", "SELECT INBOX"), " "); !strings.Contains(got, "1 EXISTS") {
		t.Errorf("after MOVE, INBOX = %q, want 1 EXISTS (source lost the message)", got)
	}
	if got := strings.Join(c.mustOK("a6", "SELECT Archive"), " "); !strings.Contains(got, "1 EXISTS") {
		t.Errorf("after MOVE, Archive = %q, want 1 EXISTS (destination gained it)", got)
	}
}

// TestIMAPSpecialUse covers SPECIAL-USE (RFC 6154): LIST tags the well-known
// folders with \Sent/\Drafts/\Trash/\Junk so clients auto-discover them.
func TestIMAPSpecialUse(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")

	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "SPECIAL-USE") {
		t.Errorf("CAPABILITY missing SPECIAL-USE: %s", caps)
	}

	joined := strings.Join(c.mustOK("a2", `LIST "" "*"`), "\n")
	for _, want := range []string{`\Sent`, `\Drafts`, `\Trash`, `\Junk`} {
		if !strings.Contains(joined, want) {
			t.Errorf("LIST missing the SPECIAL-USE attribute %q in:\n%s", want, joined)
		}
	}
}

// TestIMAPAuthLogin covers the AUTHENTICATE LOGIN SASL mechanism: the base64
// username/password challenge exchange, and AUTH=LOGIN in the capability list.
func TestIMAPAuthLogin(t *testing.T) {
	c, _ := startServer(t)
	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "AUTH=LOGIN") {
		t.Errorf("CAPABILITY missing AUTH=LOGIN: %s", caps)
	}

	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	fmt.Fprintf(c.conn, "a1 AUTHENTICATE LOGIN\r\n")
	if l := c.line(); l != "+ "+enc("Username:") {
		t.Fatalf("username challenge = %q, want %q", l, "+ "+enc("Username:"))
	}
	fmt.Fprintf(c.conn, "%s\r\n", enc("alice"))
	if l := c.line(); l != "+ "+enc("Password:") {
		t.Fatalf("password challenge = %q, want %q", l, "+ "+enc("Password:"))
	}
	fmt.Fprintf(c.conn, "%s\r\n", enc("secret"))
	if _, status := c.collect("a1"); status != "OK" {
		t.Fatalf("AUTHENTICATE LOGIN status = %s, want OK", status)
	}

	// Authenticated: a TRANSACTION command now works.
	c.mustOK("a2", "SELECT INBOX")
}

// TestIMAPQuota covers QUOTA (RFC 2087): GETQUOTAROOT/GETQUOTA report the mailbox
// STORAGE quota read from the store, and SETQUOTA is refused for a regular client.
func TestIMAPQuota(t *testing.T) {
	c, path := startServer(t)
	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "QUOTA") {
		t.Errorf("CAPABILITY missing QUOTA: %s", caps)
	}

	// Set a storage quota directly in the store before login.
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuota(objectstore.QuotaLimits{StorageKB: 500000}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	c.mustOK("a1", "LOGIN alice secret")

	joined := strings.Join(c.mustOK("a2", "GETQUOTAROOT INBOX"), "\n")
	if !strings.Contains(joined, "QUOTAROOT") || !strings.Contains(joined, "INBOX") {
		t.Errorf("GETQUOTAROOT missing the QUOTAROOT line: %s", joined)
	}
	if !strings.Contains(joined, "STORAGE") || !strings.Contains(joined, "500000") {
		t.Errorf("GETQUOTAROOT missing STORAGE quota 500000: %s", joined)
	}

	if got := strings.Join(c.mustOK("a3", `GETQUOTA ""`), " "); !strings.Contains(got, "STORAGE") {
		t.Errorf("GETQUOTA missing STORAGE: %q", got)
	}

	if _, status := c.do("a4", `SETQUOTA "" (STORAGE 999999)`); status != "NO" {
		t.Errorf("SETQUOTA = %s, want NO (not permitted for a client)", status)
	}
}

// TestIMAPESearch covers ESEARCH (RFC 4731): SEARCH RETURN (...) yields an ESEARCH
// response with only the requested result options.
func TestIMAPESearch(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX") // two messages, seq 1 and 2

	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "ESEARCH") {
		t.Errorf("CAPABILITY missing ESEARCH: %s", caps)
	}

	un, status := c.do("a3", "SEARCH RETURN (MIN MAX COUNT ALL) ALL")
	if status != "OK" {
		t.Fatalf("ESEARCH status = %s, want OK", status)
	}
	joined := strings.Join(un, " ")
	if !strings.Contains(joined, "ESEARCH") {
		t.Fatalf("want an ESEARCH response, got %v", un)
	}
	for _, want := range []string{"COUNT 2", "MIN 1", "MAX 2", "ALL 1:2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ESEARCH missing %q in %q", want, joined)
		}
	}
}

// TestIMAPMultiAppend covers MULTIAPPEND (RFC 3502): one APPEND carrying two
// message literals stores both and reports a uid-set in APPENDUID.
func TestIMAPMultiAppend(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX") // two messages already

	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "MULTIAPPEND") {
		t.Errorf("CAPABILITY missing MULTIAPPEND: %s", caps)
	}

	m1, m2 := "Subject: m1\r\n\r\nb1", "Subject: m2\r\n\r\nb2"
	fmt.Fprintf(c.conn, "a3 APPEND INBOX {%d}\r\n", len(m1))
	if l := c.line(); !strings.HasPrefix(l, "+") {
		t.Fatalf("first continuation = %q, want +", l)
	}
	fmt.Fprintf(c.conn, "%s {%d}\r\n", m1, len(m2))
	if l := c.line(); !strings.HasPrefix(l, "+") {
		t.Fatalf("second continuation = %q, want +", l)
	}
	fmt.Fprintf(c.conn, "%s\r\n", m2)
	if a3 := c.collectTagged("a3"); !strings.Contains(a3, "[APPENDUID ") {
		t.Errorf("MULTIAPPEND = %q, want an [APPENDUID ...] response code", a3)
	}

	// Both messages landed: the two originals plus the two appended.
	if got := strings.Join(c.mustOK("a4", "SELECT INBOX"), " "); !strings.Contains(got, "4 EXISTS") {
		t.Errorf("after MULTIAPPEND, INBOX = %q, want 4 EXISTS", got)
	}
}

// TestIMAPSort covers SORT (RFC 5256): ordering by subject, and REVERSE.
func TestIMAPSort(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX") // seq 1 = "one", seq 2 = "two"

	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "SORT") {
		t.Errorf("CAPABILITY missing SORT: %s", caps)
	}

	if un := strings.Join(c.mustOK("a3", "SORT (SUBJECT) UTF-8 ALL"), " "); !strings.Contains(un, "SORT 1 2") {
		t.Errorf("SORT (SUBJECT) = %q, want \"SORT 1 2\" (one < two)", un)
	}
	if un := strings.Join(c.mustOK("a4", "SORT (REVERSE SUBJECT) UTF-8 ALL"), " "); !strings.Contains(un, "SORT 2 1") {
		t.Errorf("SORT (REVERSE SUBJECT) = %q, want \"SORT 2 1\"", un)
	}
}

// TestIMAPThread covers THREAD (RFC 5256): a reply nests under its parent for both
// the REFERENCES and ORDEREDSUBJECT algorithms.
func TestIMAPThread(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "CREATE Thr")

	m1 := "Message-ID: <a@x>\r\nSubject: Hello\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\nbody1"
	m2 := "Message-ID: <b@x>\r\nIn-Reply-To: <a@x>\r\nReferences: <a@x>\r\nSubject: Re: Hello\r\nDate: Mon, 01 Jan 2024 11:00:00 +0000\r\n\r\nbody2"
	if s := c.appendMsg("a3", "Thr", m1); s != "OK" {
		t.Fatalf("append m1 = %s", s)
	}
	if s := c.appendMsg("a4", "Thr", m2); s != "OK" {
		t.Fatalf("append m2 = %s", s)
	}
	c.mustOK("a5", "SELECT Thr") // seq 1 = m1, seq 2 = m2 (a reply to m1)

	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "THREAD=REFERENCES") {
		t.Errorf("CAPABILITY missing THREAD=REFERENCES: %s", caps)
	}

	if un := strings.Join(c.mustOK("a6", "THREAD REFERENCES UTF-8 ALL"), " "); !strings.Contains(un, "THREAD (1 2)") {
		t.Errorf("THREAD REFERENCES = %q, want \"THREAD (1 2)\" (2 replies to 1)", un)
	}
	if un := strings.Join(c.mustOK("a7", "THREAD ORDEREDSUBJECT UTF-8 ALL"), " "); !strings.Contains(un, "(1 2)") {
		t.Errorf("THREAD ORDEREDSUBJECT = %q, want \"(1 2)\" (same base subject)", un)
	}
}

// TestIMAPBinary covers BINARY (RFC 3516): FETCH BINARY[] returns the body decoded
// from its Content-Transfer-Encoding, and BINARY.SIZE[] reports the decoded length.
func TestIMAPBinary(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "CREATE Bin")

	// base64("hello world") in the body; the decoded content is "hello world".
	msg := "Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\naGVsbG8gd29ybGQ="
	if s := c.appendMsg("a3", "Bin", msg); s != "OK" {
		t.Fatalf("append = %s", s)
	}
	c.mustOK("a4", "SELECT Bin")

	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "BINARY") {
		t.Errorf("CAPABILITY missing BINARY: %s", caps)
	}

	if un := strings.Join(c.mustOK("a5", "FETCH 1 BINARY[]"), " "); !strings.Contains(un, "hello world") {
		t.Errorf("BINARY[] = %q, want the decoded \"hello world\"", un)
	}
	if un := strings.Join(c.mustOK("a6", "FETCH 1 BINARY.SIZE[]"), " "); !strings.Contains(un, "BINARY.SIZE[] 11") {
		t.Errorf("BINARY.SIZE[] = %q, want \"BINARY.SIZE[] 11\" (len of \"hello world\")", un)
	}
}

// TestIMAPCompress covers COMPRESS DEFLATE (RFC 4978): after activation, commands
// round-trip over the compressed link and COMPRESS=DEFLATE is no longer advertised.
func TestIMAPCompress(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")

	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "COMPRESS=DEFLATE") {
		t.Errorf("CAPABILITY missing COMPRESS=DEFLATE: %s", caps)
	}

	// Activate compression; the tagged OK arrives uncompressed.
	fmt.Fprintf(c.conn, "a2 COMPRESS DEFLATE\r\n")
	if _, status := c.collect("a2"); status != "OK" {
		t.Fatalf("COMPRESS status = %s, want OK", status)
	}

	// Both directions are DEFLATE now; wrap the client I/O to match.
	fw, err := flate.NewWriter(c.conn, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	c.br = bufio.NewReader(flate.NewReader(c.br))

	// A command over the compressed link must round-trip.
	fmt.Fprintf(fw, "a3 CAPABILITY\r\n")
	if err := fw.Flush(); err != nil {
		t.Fatal(err)
	}
	un, status := c.collect("a3")
	if status != "OK" {
		t.Fatalf("compressed CAPABILITY status = %s, want OK", status)
	}
	joined := strings.Join(un, " ")
	if !strings.Contains(joined, "IMAP4rev1") {
		t.Errorf("compressed CAPABILITY = %q, want IMAP4rev1", joined)
	}
	if strings.Contains(joined, "COMPRESS=DEFLATE") {
		t.Errorf("COMPRESS=DEFLATE still advertised after activation: %q", joined)
	}
}

// TestIMAPCondstore covers CONDSTORE (RFC 7162): HIGHESTMODSEQ in SELECT, MODSEQ in
// FETCH, the UNCHANGEDSINCE conditional STORE with a [MODIFIED] rejection, ENABLE,
// and HIGHESTMODSEQ in STATUS.
func TestIMAPCondstore(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")

	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "CONDSTORE") || !strings.Contains(caps, "ENABLE") {
		t.Errorf("CAPABILITY missing CONDSTORE/ENABLE: %s", caps)
	}

	if sel := strings.Join(c.mustOK("a2", "SELECT INBOX (CONDSTORE)"), " "); !strings.Contains(sel, "HIGHESTMODSEQ") {
		t.Errorf("SELECT (CONDSTORE) missing HIGHESTMODSEQ: %s", sel)
	}

	// FETCH MODSEQ yields the message's modseq.
	m0 := parseModseq(t, strings.Join(c.mustOK("a3", "FETCH 1 (MODSEQ)"), " "))

	// A real flag change advances the modseq past m0.
	c.mustOK("a4", `STORE 1 +FLAGS (\Flagged)`)

	// A conditional STORE gated on the now-stale m0 is rejected and named in
	// [MODIFIED], and it must not have applied \Answered.
	line := c.taggedLine("a5", fmt.Sprintf(`STORE 1 (UNCHANGEDSINCE %d) +FLAGS (\Answered)`, m0))
	if !strings.Contains(line, "MODIFIED 1") {
		t.Errorf("stale conditional STORE = %q, want a [MODIFIED 1] response code", line)
	}
	if flags := strings.Join(c.mustOK("a6", "FETCH 1 (FLAGS)"), " "); strings.Contains(flags, `\Answered`) {
		t.Errorf("rejected conditional STORE applied \\Answered anyway: %q", flags)
	}

	if en := strings.Join(c.mustOK("a7", "ENABLE CONDSTORE"), " "); !strings.Contains(en, "ENABLED CONDSTORE") {
		t.Errorf("ENABLE CONDSTORE = %q, want ENABLED CONDSTORE", en)
	}

	if st := strings.Join(c.mustOK("a8", "STATUS INBOX (HIGHESTMODSEQ)"), " "); !strings.Contains(st, "HIGHESTMODSEQ") {
		t.Errorf("STATUS = %q, want HIGHESTMODSEQ", st)
	}
}

// TestIMAPQResync covers QRESYNC (RFC 7162): EXPUNGE is reported as VANISHED once
// enabled, and a fresh SELECT (QRESYNC ...) reports the UIDs gone since the client's
// modseq as VANISHED (EARLIER).
func TestIMAPQResync(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")

	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "QRESYNC") {
		t.Errorf("CAPABILITY missing QRESYNC: %s", caps)
	}

	// Baseline: learn the mailbox UIDVALIDITY and HIGHESTMODSEQ before any expunge.
	sel := strings.Join(c.mustOK("a2", "SELECT INBOX (CONDSTORE)"), "\n")
	uidv := parseNumAfter(t, sel, "UIDVALIDITY ")
	base := parseNumAfter(t, sel, "HIGHESTMODSEQ ")

	// With QRESYNC on, an expunge is reported as VANISHED, not per-message EXPUNGE.
	c.mustOK("a3", "ENABLE QRESYNC")
	c.mustOK("a4", `STORE 1 +FLAGS (\Deleted)`)
	exp := strings.Join(c.mustOK("a5", "EXPUNGE"), " ")
	if !strings.Contains(exp, "VANISHED 1") || strings.Contains(exp, "1 EXPUNGE") {
		t.Errorf("QRESYNC EXPUNGE = %q, want \"VANISHED 1\" and no per-message EXPUNGE", exp)
	}

	// A fresh SELECT (QRESYNC ...) with the pre-expunge modseq names the gone UID.
	rsel := strings.Join(c.mustOK("a6", fmt.Sprintf("SELECT INBOX (QRESYNC (%d %d))", uidv, base)), "\n")
	if !strings.Contains(rsel, "VANISHED (EARLIER) 1") {
		t.Errorf("SELECT (QRESYNC) = %q, want \"VANISHED (EARLIER) 1\"", rsel)
	}
}

// parseNumAfter extracts the run of digits immediately following prefix.
func parseNumAfter(t *testing.T, s, prefix string) uint64 {
	t.Helper()
	i := strings.Index(s, prefix)
	if i < 0 {
		t.Fatalf("no %q in %q", prefix, s)
	}
	rest := s[i+len(prefix):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	n, err := strconv.ParseUint(rest[:j], 10, 64)
	if err != nil {
		t.Fatalf("bad number after %q in %q: %v", prefix, s, err)
	}
	return n
}

// TestIMAPACL covers ACL (RFC 4314): MYRIGHTS, granting and adjusting a member's
// rights via SETACL, reading them with GETACL, and revoking with DELETEACL.
func TestIMAPACL(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")

	if caps := strings.Join(c.mustOK("a0", "CAPABILITY"), " "); !strings.Contains(caps, "ACL") {
		t.Errorf("CAPABILITY missing ACL: %s", caps)
	}

	// The owner holds the administer right on their own mailbox.
	if myr := strings.Join(c.mustOK("a2", "MYRIGHTS INBOX"), " "); !strings.Contains(myr, "MYRIGHTS") || !strings.Contains(myr, "a") {
		t.Errorf("MYRIGHTS = %q, want the owner to hold administer (a)", myr)
	}

	// Grant bob read; GETACL reads it back.
	c.mustOK("a3", "SETACL INBOX bob lr")
	if got := aclRightsOf(strings.Join(c.mustOK("a4", "GETACL INBOX"), " "), "bob"); !strings.Contains(got, "r") || !strings.Contains(got, "l") {
		t.Errorf("bob rights after grant = %q, want at least lr", got)
	}

	// +w adds the write right without dropping the existing ones.
	c.mustOK("a5", "SETACL INBOX bob +w")
	if got := aclRightsOf(strings.Join(c.mustOK("a6", "GETACL INBOX"), " "), "bob"); !strings.Contains(got, "w") || !strings.Contains(got, "r") {
		t.Errorf("bob rights after +w = %q, want w added to r", got)
	}

	if lr := strings.Join(c.mustOK("a7", "LISTRIGHTS INBOX bob"), " "); !strings.Contains(lr, "LISTRIGHTS") {
		t.Errorf("LISTRIGHTS = %q, want a LISTRIGHTS response", lr)
	}

	// Revoke bob entirely.
	c.mustOK("a8", "DELETEACL INBOX bob")
	if acl := strings.Join(c.mustOK("a9", "GETACL INBOX"), " "); strings.Contains(acl, `"bob"`) {
		t.Errorf("GETACL after DELETEACL = %q, want bob removed", acl)
	}
}

// aclRightsOf returns the rights token following the quoted identifier in an ACL
// response, or "".
func aclRightsOf(acl, id string) string {
	marker := `"` + id + `" `
	i := strings.Index(acl, marker)
	if i < 0 {
		return ""
	}
	rest := acl[i+len(marker):]
	if j := strings.IndexByte(rest, ' '); j >= 0 {
		return rest[:j]
	}
	return rest
}

// parseModseq extracts the n from the first "MODSEQ (n)" in an IMAP response.
func parseModseq(t *testing.T, s string) uint64 {
	t.Helper()
	i := strings.Index(s, "MODSEQ (")
	if i < 0 {
		t.Fatalf("no MODSEQ in %q", s)
	}
	rest := s[i+len("MODSEQ ("):]
	j := strings.IndexByte(rest, ')')
	if j < 0 {
		t.Fatalf("unterminated MODSEQ in %q", s)
	}
	n, err := strconv.ParseUint(rest[:j], 10, 64)
	if err != nil {
		t.Fatalf("bad MODSEQ %q: %v", rest[:j], err)
	}
	return n
}
