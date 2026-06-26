package imap

import (
	"encoding/base64"
	"fmt"
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
