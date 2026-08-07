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
	readerDir = t.TempDir()
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
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH = h[0]
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, msgEID), []uint32{logonH, 0xFFFFFFFF})
	msgH = h[1]
	return sess, logonH, msgH, readerDir
}

// TestReadReceiptFailureIsRecorded is the defect. Every branch of the receipt path
// is swallowed so it can never fail the read that triggered it, which is right and
// is also what makes the failure invisible: the sender is simply never told their
// message was read. The only trace went to stderr, and the operator's log viewer
// reads the central store, so a systematically broken send path showed nothing at
// all.
func TestReadReceiptFailureIsRecorded(t *testing.T) {
	sink := &ropSink{}
	sess, logonH, msgH, readerDir := brokenSenderSession(t, sink)
	defer sess.Close()

	sess.Dispatch(buildSetMessageReadFlag(0, 1, rfDefault), []uint32{logonH, msgH})

	e, ok := sink.find("readreceipt.fail")
	if !ok {
		t.Fatal("a failed read receipt left no record, so nothing says the sender was never notified")
	}
	if e.Level != logging.LevelError || e.Subsystem != logging.MAPI {
		t.Errorf("event = %s/%s, want error/mapi", e.Level, e.Subsystem)
	}
	// Which of the three steps failed, or a broken send is indistinguishable from a
	// store error.
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

	buf, _ := sess.Dispatch(buildSetMessageReadFlag(0, 1, rfDefault), []uint32{logonH, msgH})
	if len(buf) == 0 {
		t.Fatal("SetMessageReadFlag produced no response")
	}
	// The response's status word is the last 4 bytes of the ROP's reply; a success
	// is ecSuccess. Reading it exactly is the point: a failure here would mean the
	// receipt's problem escaped onto the client's read.
	if got := buf[len(buf)-4:]; got[0] != 0 || got[1] != 0 || got[2] != 0 || got[3] != 0 {
		t.Errorf("SetMessageReadFlag returned a non-zero status %v, so the receipt failure escaped", got)
	}
}

// TestSuccessfulReadReceiptRecordsNoFailure guards the other direction. A path that
// works must stay quiet, or the event becomes noise an operator learns to skip.
func TestSuccessfulReadReceiptRecordsNoFailure(t *testing.T) {
	sink := &ropSink{}
	readerDir, senderDir := t.TempDir(), t.TempDir()
	accounts := directory.StaticAccounts{
		"reader@hermex.test": {MailboxPath: readerDir},
		"sender@hermex.test": {MailboxPath: senderDir},
	}
	msgID := seedReceiptRequest(t, readerDir, "sender@hermex.test", "PingMe")
	sess := NewSession(readerDir, accounts, "reader@hermex.test", WithLogger(logging.New(sink)))
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenMessage(0, 1,
		uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox)),
		uint64(mapi.MakeEIDEx(1, uint64(msgID)))), []uint32{logonH, 0xFFFFFFFF})
	sess.Dispatch(buildSetMessageReadFlag(0, 1, rfDefault), []uint32{logonH, h[1]})

	if n := inboxCount(t, senderDir); n != 1 {
		t.Fatalf("sender inbox = %d, want the receipt to have been delivered", n)
	}
	if e, ok := sink.find("readreceipt.fail"); ok {
		t.Errorf("a delivered receipt was recorded as failed: %v %s", e.Fields, e.Err)
	}
}
