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
		return mta.Deliver(accounts, senderOf(raw), recipients, raw, when)
	}

	sweepOutboxes(accounts, deliver, nil, nil)

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

// TestGuardedSweepRefusalLeavesTheOutboxAlone proves a second MTA instance, told
// another one is already sweeping, does not touch any mailbox. Running anyway
// would re-deliver a scheduled message in the window between its delivery and its
// removal from the Outbox, which is the race the lock exists to close.
func TestGuardedSweepRefusalLeavesTheOutboxAlone(t *testing.T) {
	root := t.TempDir()
	aliceDir := filepath.Join(root, "alice")
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

	accounts := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: aliceDir}}
	delivered := 0
	deliver := func(recipients []string, raw []byte, when time.Time) ([]string, error) {
		delivered++
		return nil, nil
	}

	guardedSweep(accounts, deliver, nil, func() (func(), bool) { return nil, false }, nil)
	if delivered != 0 {
		t.Errorf("a refused sweep delivered %d message(s), want none", delivered)
	}
	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDOutbox)); n != 1 {
		t.Errorf("alice Outbox has %d after a refused sweep, want the message untouched", n)
	}

	// Permitted: the sweep runs and hands the permission back.
	released := 0
	guardedSweep(accounts, deliver, nil, func() (func(), bool) { return func() { released++ }, true }, nil)
	if delivered != 1 {
		t.Errorf("a permitted sweep delivered %d message(s), want 1", delivered)
	}
	if released != 1 {
		t.Errorf("permission released %d times, want 1", released)
	}
}
