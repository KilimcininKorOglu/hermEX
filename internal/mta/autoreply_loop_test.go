package mta

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// when is a fixed delivery time; the test mailboxes use an open-ended
// out-of-office window, so any time is inside it.
var when = time.Unix(1700000000, 0)

func listInbox(t *testing.T, path string) []objectstore.MessageInfo {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

// enableOOF opens a fresh mailbox at path and turns on out-of-office.
func enableOOF(t *testing.T, path string) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetOOFSettings(objectstore.OOFSettings{
		Enabled: true, Subject: "Away", InternalReply: "Away, back later.",
	}); err != nil {
		t.Fatal(err)
	}
}

// TestTwoMailboxOOFNoStorm is the loop-break proof. Two local mailboxes both
// have out-of-office enabled; one mails the other. The exchange must terminate
// after exactly one auto-reply: bob auto-replies to alice's message, and alice
// does NOT auto-reply to bob's auto-reply because it carries
// Auto-Submitted: auto-replied. A broken loop would recurse and pile auto-replies
// into both inboxes (or overflow the stack).
func TestTwoMailboxOOFNoStorm(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "alice")
	pathB := filepath.Join(dir, "bob")
	enableOOF(t, pathA)
	enableOOF(t, pathB)
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: pathA},
		"bob@hermex.test":   {MailboxPath: pathB},
	}

	orig := []byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: hi\r\n\r\nhello\r\n")
	if err := deliver(accounts, "alice@hermex.test", "bob@hermex.test", pathB, orig, when); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	if b := listInbox(t, pathB); len(b) != 1 {
		t.Fatalf("bob inbox = %d, want 1 (alice's original; no reply-to-the-reply)", len(b))
	}
	aInbox := listInbox(t, pathA)
	if len(aInbox) != 1 {
		t.Fatalf("alice inbox = %d, want 1 (bob's single auto-reply)", len(aInbox))
	}
	// The single message is bob's auto-reply: its sender is bob and its subject
	// is the configured out-of-office subject, not alice's original "hi".
	if !strings.Contains(aInbox[0].Sender, "bob@hermex.test") {
		t.Errorf("alice's message is from %q, want bob's auto-reply", aInbox[0].Sender)
	}
	if aInbox[0].Subject != "Away" {
		t.Errorf("alice's message subject = %q, want the out-of-office subject", aInbox[0].Subject)
	}
	// The termination itself proves the loop break worked on the wire: alice's
	// delivery saw bob's reply carrying Auto-Submitted: auto-replied (TestBuildAutoReply
	// covers the builder) and suppressed, so no reply-to-the-reply was sent. The
	// re-synthesized stored form does not re-emit Auto-Submitted (oxcmail.Export
	// writes a fixed header set), which is harmless: suppression reads the raw
	// delivery bytes, never the stored copy.
}

// TestDeliverNoAutoReplyWhenOff is the negative control: a mailbox with
// out-of-office off must send no reply.
func TestDeliverNoAutoReplyWhenOff(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "alice")
	pathB := filepath.Join(dir, "bob")
	// Provision both; leave bob's out-of-office off (the default).
	for _, p := range []string{pathA, pathB} {
		st, err := objectstore.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: pathA},
		"bob@hermex.test":   {MailboxPath: pathB},
	}

	orig := []byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: hi\r\n\r\nhello\r\n")
	if err := deliver(accounts, "alice@hermex.test", "bob@hermex.test", pathB, orig, when); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if a := listInbox(t, pathA); len(a) != 0 {
		t.Errorf("alice inbox = %d, want 0 (bob's out-of-office is off)", len(a))
	}
}

// TestDeliverNoAutoReplyToAutomatedSender proves the suppression is wired into
// the delivery path, not just the unit: a message that itself carries
// Auto-Submitted must draw no reply even though the recipient has out-of-office
// on. This is the rule that prevents two autoresponders from looping.
func TestDeliverNoAutoReplyToAutomatedSender(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "alice")
	pathB := filepath.Join(dir, "bob")
	enableOOF(t, pathB)
	for _, p := range []string{pathA} {
		st, err := objectstore.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: pathA},
		"bob@hermex.test":   {MailboxPath: pathB},
	}

	orig := []byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nAuto-Submitted: auto-generated\r\nSubject: bounce\r\n\r\nbody\r\n")
	if err := deliver(accounts, "alice@hermex.test", "bob@hermex.test", pathB, orig, when); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if a := listInbox(t, pathA); len(a) != 0 {
		t.Errorf("alice inbox = %d, want 0 (the incoming message was automated)", len(a))
	}
}
