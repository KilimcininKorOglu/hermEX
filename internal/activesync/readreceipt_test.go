package activesync

import (
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// receiptServer builds an ActiveSync server whose user's Inbox holds one message
// from bob asking for a read receipt, with bob as a local mailbox. It returns the
// server, the user's mailbox dir and bob's.
func receiptServer(t *testing.T) (ts *httptest.Server, dir, bobDir string) {
	t.Helper()
	return receiptServerTo(t, "bob@hermex.test")
}

// receiptServerTo is receiptServer with the receipt requested to notifyTo, while the
// Return-Path stays bob.
func receiptServerTo(t *testing.T, notifyTo string) (ts *httptest.Server, dir, bobDir string) {
	t.Helper()
	dir, bobDir = filepath.Join(t.TempDir(), "mbox"), filepath.Join(t.TempDir(), "bob")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// As delivered: the Return-Path is bob, so an automatic receipt may answer him.
	raw := "Return-Path: <bob@hermex.test>\r\nFrom: Bob <bob@hermex.test>\r\nTo: " + testUser + "\r\nSubject: Did you read this\r\n" +
		"Message-ID: <r1@hermex.test>\r\nDisposition-Notification-To: " + notifyTo + "\r\n\r\nplease confirm\r\n"
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	st.Close()
	accs := directory.StaticAccounts{
		testUser:            {Password: testPass, MailboxPath: dir},
		"bob@hermex.test":   {Password: "x", MailboxPath: bobDir},
		"carol@hermex.test": {Password: "x", MailboxPath: filepath.Join(filepath.Dir(bobDir), "carol")},
	}
	ts = httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, dir, bobDir
}

// markFirstRead syncs the Inbox and sends a Change that marks its first message
// read.
func markFirstRead(t *testing.T, ts *httptest.Server, dir string) {
	t.Helper()
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", ""))
	change := wbxml.Elem(wbxml.ASChange,
		wbxml.Str(wbxml.ASServerID, strconv.FormatUint(uint64(firstUID(t, dir)), 10)),
		wbxml.Elem(wbxml.ASData, wbxml.Str(wbxml.EMRead, "1")))
	postCommand(t, ts, "Sync", syncReq("2", "", change))
}

// inboxCount counts the messages in a mailbox's Inbox.
func inboxCount(t *testing.T, dir string) int {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

// TestSyncReadSendsReceipt proves marking a message read on a device sends the
// receipt it asks for, as Exchange does for an ActiveSync read.
func TestSyncReadSendsReceipt(t *testing.T) {
	ts, dir, bobDir := receiptServer(t)
	markFirstRead(t, ts, dir)
	if n := inboxCount(t, bobDir); n != 1 {
		t.Errorf("receipts delivered = %d, want 1", n)
	}
}

// TestSyncReadReceiptOnlyOnceAndOnlyToTheSender proves the two limits RFC 8098 puts
// on an automatic receipt. Section 2.1: when Disposition-Notification-To names an
// address other than the Return-Path, the sender may be steering receipts at a
// third party, so none is sent without asking the reader. Section 2.1 also allows
// one receipt per message, so a second read sends nothing.
func TestSyncReadReceiptOnlyOnceAndOnlyToTheSender(t *testing.T) {
	ts, dir, bobDir := receiptServerTo(t, "carol@hermex.test")
	markFirstRead(t, ts, dir)
	carol := filepath.Join(filepath.Dir(bobDir), "carol")
	if n := inboxCountIfAny(t, carol) + inboxCountIfAny(t, bobDir); n != 0 {
		t.Errorf("receipts delivered for a request to a third party = %d, want 0", n)
	}

	ts, dir, bobDir = receiptServer(t)
	markFirstRead(t, ts, dir)
	unread := wbxml.Elem(wbxml.ASChange,
		wbxml.Str(wbxml.ASServerID, strconv.FormatUint(uint64(firstUID(t, dir)), 10)),
		wbxml.Elem(wbxml.ASData, wbxml.Str(wbxml.EMRead, "0")))
	_, root := postCommand(t, ts, "Sync", syncReq("3", "", unread))
	read := wbxml.Elem(wbxml.ASChange,
		wbxml.Str(wbxml.ASServerID, strconv.FormatUint(uint64(firstUID(t, dir)), 10)),
		wbxml.Elem(wbxml.ASData, wbxml.Str(wbxml.EMRead, "1")))
	postCommand(t, ts, "Sync", syncReq(respColl(t, root).ChildText(wbxml.ASSyncKey), "", read))
	if n := inboxCount(t, bobDir); n != 1 {
		t.Errorf("receipts delivered after reading twice = %d, want 1", n)
	}
}

// inboxCountIfAny is inboxCount for a mailbox that may never have been created.
func inboxCountIfAny(t *testing.T, dir string) int {
	t.Helper()
	st, err := objectstore.OpenExisting(dir)
	if errors.Is(err, objectstore.ErrNotProvisioned) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

// TestSyncReadReceiptSuppressed proves a mailbox that turned off receipts for
// ActiveSync reads sends none.
func TestSyncReadReceiptSuppressed(t *testing.T) {
	ts, dir, bobDir := receiptServer(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetReadReceiptConfig(objectstore.ReadReceiptConfig{SuppressActiveSync: true}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	markFirstRead(t, ts, dir)
	if n := inboxCount(t, bobDir); n != 0 {
		t.Errorf("receipts delivered = %d, want 0 with ActiveSync receipts turned off", n)
	}
}
