package rop

import (
	"os"
	"path/filepath"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
)

// brokenSenderSession is readReceiptSession with the represented sender resolving
// to a path that cannot be a mailbox, so the receipt's delivery fails. That is the
// shape of the failure the report cares about: the send path is broken and every
// receipt is silently dropped.
func brokenSenderSession(t *testing.T, sink logging.Sink) (sess *Session, logonH, msgH uint32, readerDir string) {
	t.Helper()
	readerDir = provisionedDir(t)
	// A regular file where a mailbox directory belongs.
	blocked := filepath.Join(t.TempDir(), "not-a-mailbox")
	if err := os.WriteFile(blocked, []byte("this is a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	accounts := directory.StaticAccounts{
		"reader@hermex.test": {MailboxPath: readerDir},
		"sender@hermex.test": {MailboxPath: blocked},
	}
	msgID := seedReceiptRequest(t, readerDir, "sender@hermex.test", "PingMe")
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))

	sess = NewSession(readerDir, accounts, "reader@hermex.test", WithLogger(logging.New(sink)))
	logonH = dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	msgH = dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, msgEID), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	return sess, logonH, msgH, readerDir
}

// TestReadReceiptFailureIsRecorded verifies that when sending a read receipt
// fails on the best-effort path, readreceipt.fail is emitted at error/mapi with
// stage, mailbox, user, and cause, while invalid handles are refused without logging
// a receipt failure.
func TestReadReceiptFailureIsRecorded(t *testing.T) {
	sink := &ropSink{}
	sess, logonH, msgH, readerDir := brokenSenderSession(t, sink)
	defer sess.Close()

	// Hostile null handle must return ecError without generating a readreceipt.fail event.
	wantEC(t, dispatchOn(sess, buildSetMessageReadFlag(0, 1, rfDefault), logonH, 0xFFFFFFFF), ropSetMessageReadFlag, ecError, "SetMessageReadFlag(null handle)")
	if _, ok := sink.find("readreceipt.fail"); ok {
		t.Fatal("refused SetMessageReadFlag logged a readreceipt.fail event")
	}

	ropOK(t, dispatchOn(sess, buildSetMessageReadFlag(0, 1, rfDefault), logonH, msgH), ropSetMessageReadFlag, "SetMessageReadFlag(rfDefault)")

	e, ok := sink.find("readreceipt.fail")
	if !ok {
		t.Fatal("a failed read receipt left no record, so nothing says the sender was never notified")
	}
	if e.Level != logging.LevelError || e.Subsystem != logging.MAPI {
		t.Errorf("event = %s/%s, want error/mapi", e.Level, e.Subsystem)
	}
	if e.Fields["stage"] != "send" {
		t.Errorf("stage = %v, want send", e.Fields["stage"])
	}
	if e.Fields["mailbox"] != readerDir {
		t.Errorf("mailbox = %v, want %q", e.Fields["mailbox"], readerDir)
	}
	if e.User != "reader@hermex.test" {
		t.Errorf("user = %q, want the reader", e.User)
	}
	if e.Err == "" {
		t.Error("the record carries no cause")
	}
}

// TestReadReceiptFailureDoesNotFailTheRead proves recording the failure did not
// change the swallowing. A read receipt is best effort precisely so a send problem
// cannot make a client's read fail.
func TestReadReceiptFailureDoesNotFailTheRead(t *testing.T) {
	sink := &ropSink{}
	sess, logonH, msgH, _ := brokenSenderSession(t, sink)
	defer sess.Close()

	// ReturnValue follows RopId and the handle index ([MS-OXCMSG] 2.2.3.10.2); a
	// non-success there would mean the receipt's problem escaped onto the read.
	ropOK(t, dispatchOn(sess, buildSetMessageReadFlag(0, 1, rfDefault), logonH, msgH), ropSetMessageReadFlag, "SetMessageReadFlag with a failing receipt")
}

// TestSuccessfulReadReceiptRecordsNoFailure guards the other direction. A path that
// works must stay quiet, or the event becomes noise an operator learns to skip;
// rfSuppressReceipt suppresses delivery completely.
func TestSuccessfulReadReceiptRecordsNoFailure(t *testing.T) {
	sink := &ropSink{}
	readerDir, senderDir := provisionedDir(t), provisionedDir(t)
	accounts := directory.StaticAccounts{
		"reader@hermex.test": {MailboxPath: readerDir},
		"sender@hermex.test": {MailboxPath: senderDir},
	}
	msgID := seedReceiptRequest(t, readerDir, "sender@hermex.test", "PingMe")
	sess := NewSession(readerDir, accounts, "reader@hermex.test", WithLogger(logging.New(sink)))
	defer sess.Close()

	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1,
		uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox)),
		uint64(mapi.MakeEIDEx(1, uint64(msgID)))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	ropOK(t, dispatchOn(sess, buildSetMessageReadFlag(0, 1, rfDefault), logonH, msgH), ropSetMessageReadFlag, "SetMessageReadFlag(rfDefault)")

	if n := inboxCount(t, senderDir); n != 1 {
		t.Fatalf("sender inbox = %d, want the receipt to have been delivered", n)
	}
	if e, ok := sink.find("readreceipt.fail"); ok {
		t.Errorf("a delivered receipt was recorded as failed: %v %s", e.Fields, e.Err)
	}
}
