package imap

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// fillPastQuota sets a 1 KiB receive quota on the mailbox at path and stores a
// message large enough to pass it, the state of a mailbox that is full.
func fillPastQuota(t *testing.T, path string) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	big := "Subject: big\r\n\r\n" + strings.Repeat("x", 4096)
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(big), time.Unix(3, 0), 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuota(objectstore.QuotaLimits{ReceiveKB: 1}); err != nil {
		t.Fatal(err)
	}
}

// TestAWriteIntoAFullMailboxIsRefusedAsOverQuota fills the mailbox past its
// receive quota, the limit past which Exchange lets nothing new into a mailbox.
// RFC 9208 4.3 has a server refuse such an APPEND or COPY with NO [OVERQUOTA]. A
// MOVE inside the same mailbox adds nothing, so it must still succeed: a full
// mailbox has to be able to file its mail away to free space.
func TestAWriteIntoAFullMailboxIsRefusedAsOverQuota(t *testing.T) {
	c, path := startServer(t)
	fillPastQuota(t, path)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "CREATE Projects")
	c.mustOK("a3", "SELECT INBOX")

	if reply := appendMessage(t, c, "a4", "Subject: more\r\n\r\nbody"); !strings.HasPrefix(reply, "a4 NO [OVERQUOTA]") {
		t.Errorf("APPEND into a full mailbox = %q, want NO [OVERQUOTA]", reply)
	}
	if reply := c.taggedLine("a5", "COPY 1 Projects"); !strings.HasPrefix(reply, "a5 NO [OVERQUOTA]") {
		t.Errorf("COPY inside a full mailbox = %q, want NO [OVERQUOTA]", reply)
	}
	// The refused writes stored nothing: the Inbox still holds the two seeded
	// messages plus the one that filled it, and Projects is still empty.
	assertMessageCount(t, c, "a5a", "INBOX", 3)
	assertMessageCount(t, c, "a5b", "Projects", 0)
	if _, status := c.do("a6", "MOVE 1 Projects"); status != "OK" {
		t.Errorf("MOVE inside a full mailbox = %s, want OK", status)
	}
}

// assertMessageCount reads STATUS (MESSAGES) for mailbox and fails unless it
// reports want (RFC 3501 6.3.10).
func assertMessageCount(t *testing.T, c *testClient, tag, mailbox string, want int) {
	t.Helper()
	untagged := c.mustOK(tag, "STATUS "+mailbox+" (MESSAGES)")
	wantField := fmt.Sprintf("(MESSAGES %d)", want)
	for _, line := range untagged {
		if strings.HasPrefix(line, "* STATUS") && strings.Contains(line, wantField) {
			return
		}
	}
	t.Errorf("STATUS %s = %q, want %s", mailbox, untagged, wantField)
}
