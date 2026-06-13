package main

import (
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

// TestSweepOutboxesDeliversDueScheduledSend is a light integration test of the
// send-later worker's one-pass sweep: a past-due message in one mailbox's Outbox
// is delivered to a second mailbox through the real local-delivery path, filed to
// the sender's Sent, and removed from the Outbox.
func TestSweepOutboxesDeliversDueScheduledSend(t *testing.T) {
	root := t.TempDir()
	aliceDir := filepath.Join(root, "alice")
	bobDir := filepath.Join(root, "bob")

	// Queue a past-due scheduled send in alice's Outbox, addressed to bob.
	alice, err := objectstore.Open(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	raw := "From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: scheduled\r\n\r\nbody\r\n"
	info, err := alice.AppendMessage(int64(mapi.PrivateFIDOutbox), []byte(raw), time.Unix(1, 0), objectstore.FlagSeen)
	if err != nil {
		t.Fatal(err)
	}
	if err := alice.SetMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrDeferredSendTime, Value: mapi.UnixToNTTime(time.Now().Add(-time.Minute))},
	}); err != nil {
		t.Fatal(err)
	}
	alice.Close()
	if bob, err := objectstore.Open(bobDir); err != nil { // provision bob's mailbox
		t.Fatal(err)
	} else {
		bob.Close()
	}

	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: aliceDir},
		"bob@hermex.test":   {MailboxPath: bobDir},
	}
	deliver := func(recipients []string, raw []byte, when time.Time) ([]string, error) {
		return mta.Deliver(accounts, recipients, raw, when)
	}

	sweepOutboxes(accounts, deliver)

	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDOutbox)); n != 0 {
		t.Errorf("alice Outbox has %d after sweep, want 0 (released)", n)
	}
	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDSentItems)); n != 1 {
		t.Errorf("alice Sent has %d, want 1", n)
	}
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("bob Inbox has %d, want 1 (the scheduled send should have been delivered)", n)
	}
}

func folderCount(t *testing.T, path string, fid int64) int {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(fid)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}
