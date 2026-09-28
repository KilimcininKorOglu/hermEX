package activesync

import (
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
	dir, bobDir = filepath.Join(t.TempDir(), "mbox"), filepath.Join(t.TempDir(), "bob")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw := "From: Bob <bob@hermex.test>\r\nTo: " + testUser + "\r\nSubject: Did you read this\r\n" +
		"Message-ID: <r1@hermex.test>\r\nDisposition-Notification-To: bob@hermex.test\r\n\r\nplease confirm\r\n"
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	st.Close()
	accs := directory.StaticAccounts{
		testUser:          {Password: testPass, MailboxPath: dir},
		"bob@hermex.test": {Password: "x", MailboxPath: bobDir},
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
