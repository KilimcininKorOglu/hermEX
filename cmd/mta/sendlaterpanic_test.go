package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/spooler"
)

// queueDueSend places one past-due scheduled send to bob in a mailbox's Outbox.
func queueDueSend(t *testing.T, dir, from string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	raw := "From: " + from + "\r\nTo: bob@hermex.test\r\nSubject: scheduled\r\n\r\nbody\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDOutbox), []byte(raw), time.Unix(1, 0), objectstore.FlagSeen)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrDeferredSendTime, Value: mapi.UnixToNTTime(time.Now().Add(-time.Minute))},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAPanickingMailboxFailsOnlyItself sweeps two Outboxes and makes the release
// from one of them panic. The sweep runs in a background loop with nothing above
// it, so an unrecovered panic would end the MTA and, with the message still in
// the Outbox, end it again on every start. The other mailbox's scheduled send
// must still go out.
// wantPanicRecorded checks that a contained panic is still recorded, or the
// operator's only view shows a clean run: a sendlater.error naming the mailbox and
// the panic value, and a sendlater.panic carrying the stack, so the operator can
// tell it from an ordinary failure and find where it was raised.
func wantPanicRecorded(t *testing.T, sink *sweepSink, mailbox string) {
	t.Helper()
	if ev, ok := sink.find("sendlater.error"); !ok {
		t.Error("the contained panic left no sendlater.error event")
	} else if ev.Fields["mailbox"] != mailbox || !strings.Contains(ev.Err, "release bug") {
		t.Errorf("sendlater.error = mailbox %v, err %q; want %s and the panic value", ev.Fields["mailbox"], ev.Err, mailbox)
	}
	ev, ok := sink.find("sendlater.panic")
	if !ok {
		t.Error("the contained panic left no sendlater.panic event")
		return
	}
	if stack, isText := ev.Fields["stack"].(string); !isText || ev.Fields["mailbox"] != mailbox || !strings.Contains(stack, "goroutine") {
		t.Errorf("sendlater.panic = mailbox %v, stack %v; want %s and the goroutine stack as text", ev.Fields["mailbox"], ev.Fields["stack"], mailbox)
	}
}

func TestAPanickingMailboxFailsOnlyItself(t *testing.T) {
	root := t.TempDir()
	carolDir, aliceDir, bobDir := filepath.Join(root, "carol"), filepath.Join(root, "alice"), filepath.Join(root, "bob")
	queueDueSend(t, carolDir, "carol@hermex.test")
	queueDueSend(t, aliceDir, "alice@hermex.test")
	if bob, err := objectstore.Open(bobDir); err != nil {
		t.Fatal(err)
	} else {
		mustClose(t, bob)
	}
	accounts := directory.StaticAccounts{
		"carol@hermex.test": {MailboxPath: carolDir},
		"alice@hermex.test": {MailboxPath: aliceDir},
		"bob@hermex.test":   {MailboxPath: bobDir},
	}
	real := sendAsOwner(accounts, nil, "mail.hermex.test", logging.New(&sweepSink{}))
	bind := func(owner string) (spooler.DeliverFunc, spooler.GiveUpFunc) {
		deliver, giveUp := real(owner)
		if owner == "carol@hermex.test" {
			deliver = func([]string, []byte, time.Time) ([]string, bool, error) { panic("release bug") }
		}
		return deliver, giveUp
	}

	sink := &sweepSink{}
	sweepOutboxes(context.Background(), accounts, bind, logging.New(sink))

	wantPanicRecorded(t, sink, carolDir)

	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("bob Inbox has %d, want 1: alice's send must go out despite carol's panic", n)
	}
	if n := folderCount(t, carolDir, int64(mapi.PrivateFIDOutbox)); n != 1 {
		t.Errorf("carol Outbox has %d, want her message kept for a later sweep", n)
	}
}
