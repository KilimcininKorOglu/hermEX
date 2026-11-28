package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildGetHierarchyTableFlags builds a RopGetHierarchyTable request carrying an
// explicit TableFlags byte.
func buildGetHierarchyTableFlags(inIdx, outIdx, tableFlags uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetHierarchyTable)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(tableFlags)
	return b.Bytes()
}

// openInboxTable logs on, opens the Inbox and opens one table over it, returning
// the store and the table's handle.
func openInboxTable(t *testing.T, sess *Session, tableRop, tableFlags uint8) (*objectstore.Store, uint32) {
	t.Helper()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	st := sess.get(logonH).store

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	folderH := dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder(Inbox)", logonH, 0xFFFFFFFF)[1]

	req := buildGetContentsTableFlags(0, 1, tableFlags)
	if tableRop == ropGetHierarchyTable {
		req = buildGetHierarchyTableFlags(0, 1, tableFlags)
	}
	h := dispatchOK(t, sess, req, tableRop, "Get*Table", folderH, 0xFFFFFFFF)
	return st, h[1]
}

// wantTableChanged reads one RopNotify off a wake-up response and asserts it is a
// whole-table TABLE_CHANGED addressed to the given table handle, with no trailing
// bytes.
func wantTableChanged(t *testing.T, resp []byte, tableH uint32) {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropNotify {
		t.Fatalf("RopId = %#x, want RopNotify %#x", id, ropNotify)
	}
	if got := mustU32(t, p, "NotificationHandle"); got != tableH {
		t.Errorf("NotificationHandle = %d, want the table handle %d", got, tableH)
	}
	mustU8(t, p, "LogonId")
	if nflags := mustU16(t, p, "nflags"); nflags != fnevTableModified {
		t.Errorf("nflags = %#x, want %#x (TABLE_MODIFIED alone)", nflags, fnevTableModified)
	}
	if ev := mustU16(t, p, "TableEvent"); ev != tableChanged {
		t.Errorf("TableEvent = %#x, want %#x (TABLE_CHANGED)", ev, tableChanged)
	}
	if p.Remaining() != 0 {
		t.Errorf("trailing bytes after the RopNotify: %d", p.Remaining())
	}
}

// TestContentsTableNotifiesOnNewMessage drives the table-notification path end to
// end and also verifies invalid folder handles are refused without mutating handles
// or emitting table notifications.
func TestContentsTableNotifiesOnNewMessage(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	badResp, badHandles := sess.Dispatch(buildGetContentsTableFlags(0, 1, 0), []uint32{0xFFFFFFFF, 0xFFFFFFFF})
	wantEC(t, badResp, ropGetContentsTable, ecError, "GetContentsTable(null folder)")
	if len(badHandles) != 2 || badHandles[1] != 0xFFFFFFFF {
		t.Errorf("refused GetContentsTable handles = %#x, want slot 1 unset", badHandles)
	}

	st, tableH := openInboxTable(t, sess, ropGetContentsTable, 0)

	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox),
		[]byte("From: a@test\r\nSubject: x\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append: %v", err)
	}

	resp := dispatchOn(sess, nil)
	wantTableChanged(t, resp, tableH)

	// One change raises exactly one event: the baseline advanced with it.
	if resp2 := dispatchOn(sess, nil); len(resp2) != 0 {
		t.Errorf("second poll re-delivered %d bytes, want 0", len(resp2))
	}
}

// TestContentsTableNotifiesOnReadStateChange pins the half of the change signal
// the row count cannot carry: marking a message read moves only the modification counter.
func TestContentsTableNotifiesOnReadStateChange(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	st, tableH := openInboxTable(t, sess, ropGetContentsTable, 0)
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox),
		[]byte("From: a@test\r\nSubject: x\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if resp := dispatchOn(sess, nil); len(resp) == 0 {
		t.Fatal("the create raised no event, the read-state assertion below would be vacuous")
	}

	if err := st.SetMessageReadState(info.ID, true); err != nil {
		t.Fatalf("set read state: %v", err)
	}
	resp := dispatchOn(sess, nil)
	wantTableChanged(t, resp, tableH)
}

// TestHierarchyTableNotifiesOnNewChildFolder checks hierarchy table notifications
// and refuses GetHierarchyTable on a null folder handle.
func TestHierarchyTableNotifiesOnNewChildFolder(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	badResp, badHandles := sess.Dispatch(buildGetHierarchyTableFlags(0, 1, 0), []uint32{0xFFFFFFFF, 0xFFFFFFFF})
	wantEC(t, badResp, ropGetHierarchyTable, ecError, "GetHierarchyTable(null folder)")
	if len(badHandles) != 2 || badHandles[1] != 0xFFFFFFFF {
		t.Errorf("refused GetHierarchyTable handles = %#x, want slot 1 unset", badHandles)
	}

	st, tableH := openInboxTable(t, sess, ropGetHierarchyTable, 0)

	inbox := int64(mapi.PrivateFIDInbox)
	if _, err := st.CreateFolder(&inbox, "Project"); err != nil {
		t.Fatalf("create folder: %v", err)
	}

	resp := dispatchOn(sess, nil)
	wantTableChanged(t, resp, tableH)
}

// TestTableNotificationsHonorNoNotifications proves the NoNotifications TableFlags
// bit suppresses change events on that table.
func TestTableNotificationsHonorNoNotifications(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	st, tableH := openInboxTable(t, sess, ropGetContentsTable, tableFlagNoNotifications)
	if sess.get(tableH) == nil {
		t.Fatal("NoNotifications table handle missing from session")
	}

	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox),
		[]byte("From: a@test\r\nSubject: x\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append: %v", err)
	}

	if resp := dispatchOn(sess, nil); len(resp) != 0 {
		t.Errorf("a table opened with NoNotifications delivered %d bytes, want 0", len(resp))
	}
}

// TestTableNotificationIsNotSentForAnUnchangedFolder pins the baseline contract at
// both ends.
func TestTableNotificationIsNotSentForAnUnchangedFolder(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	st := sess.get(logonH).store
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox),
		[]byte("From: a@test\r\nSubject: x\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append: %v", err)
	}

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	folderH := dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder(Inbox)", logonH, 0xFFFFFFFF)[1]
	// RopId(1) + HandleIndex(1) + ReturnValue(4) + RowCount(4).
	const getTableReplyLen = 10
	openResp := dispatchOn(sess, buildGetContentsTableFlags(0, 1, 0), folderH, 0xFFFFFFFF)
	ropOK(t, openResp, ropGetContentsTable, "GetContentsTable")
	if len(openResp) != getTableReplyLen {
		t.Errorf("GetContentsTable response = %d bytes, want %d (the reply alone, no RopNotify)",
			len(openResp), getTableReplyLen)
	}

	if resp := dispatchOn(sess, nil); len(resp) != 0 {
		t.Errorf("an unchanged folder delivered %d bytes, want 0", len(resp))
	}
}
