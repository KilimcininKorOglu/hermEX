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
// the store and the table's handle. tableRop is RopGetContentsTable or
// RopGetHierarchyTable.
func openInboxTable(t *testing.T, sess *Session, tableRop, tableFlags uint8) (*objectstore.Store, uint32) {
	t.Helper()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	st := sess.get(logonH).store

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h = sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]

	req := buildGetContentsTableFlags(0, 1, tableFlags)
	if tableRop == ropGetHierarchyTable {
		req = buildGetHierarchyTableFlags(0, 1, tableFlags)
	}
	resp, h := sess.Dispatch(req, []uint32{folderH, 0xFFFFFFFF})
	ropOK(t, resp, tableRop, "Get*Table")
	return st, h[1]
}

// wantTableChanged reads one RopNotify off a wake-up response and asserts it is a
// whole-table TABLE_CHANGED addressed to the given table handle, with no trailing
// bytes: a TABLE_CHANGED body is the event code alone, no row ids ride with it.
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
// end: a client opens a contents table, a message is delivered into the shared
// store out of band, and the next Execute carries a TABLE_CHANGED addressed to the
// table's own handle. The client holds no subscription, which is the point, a
// table is its own notification target.
func TestContentsTableNotifiesOnNewMessage(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	st, tableH := openInboxTable(t, sess, ropGetContentsTable, 0)

	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox),
		[]byte("From: a@test\r\nSubject: x\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append: %v", err)
	}

	resp, _ := sess.Dispatch(nil, nil)
	wantTableChanged(t, resp, tableH)

	// One change raises exactly one event: the baseline advanced with it.
	if resp2, _ := sess.Dispatch(nil, nil); len(resp2) != 0 {
		t.Errorf("second poll re-delivered %d bytes, want 0", len(resp2))
	}
}

// TestContentsTableNotifiesOnReadStateChange pins the half of the change signal
// the row count cannot carry: marking a message read leaves the folder's message
// count untouched and moves only its modification counter.
func TestContentsTableNotifiesOnReadStateChange(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	st, tableH := openInboxTable(t, sess, ropGetContentsTable, 0)
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox),
		[]byte("From: a@test\r\nSubject: x\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	// Drain the create so the next event can only come from the read flip.
	if resp, _ := sess.Dispatch(nil, nil); len(resp) == 0 {
		t.Fatal("the create raised no event, the read-state assertion below would be vacuous")
	}

	if err := st.SetMessageReadState(info.ID, true); err != nil {
		t.Fatalf("set read state: %v", err)
	}
	resp, _ := sess.Dispatch(nil, nil)
	wantTableChanged(t, resp, tableH)
}

// TestHierarchyTableNotifiesOnNewChildFolder is the hierarchy half: a child
// folder created under the table's folder moves the table's rows, so the table
// handle receives a TABLE_CHANGED.
func TestHierarchyTableNotifiesOnNewChildFolder(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	st, tableH := openInboxTable(t, sess, ropGetHierarchyTable, 0)

	inbox := int64(mapi.PrivateFIDInbox)
	if _, err := st.CreateFolder(&inbox, "Project"); err != nil {
		t.Fatalf("create folder: %v", err)
	}

	resp, _ := sess.Dispatch(nil, nil)
	wantTableChanged(t, resp, tableH)
}

// TestTableNotificationsHonorNoNotifications proves the NoNotifications TableFlags
// bit is read: a client that asked to be left out gets no event for a change that
// would otherwise raise one.
func TestTableNotificationsHonorNoNotifications(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	st, _ := openInboxTable(t, sess, ropGetContentsTable, tableFlagNoNotifications)

	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox),
		[]byte("From: a@test\r\nSubject: x\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append: %v", err)
	}

	if resp, _ := sess.Dispatch(nil, nil); len(resp) != 0 {
		t.Errorf("a table opened with NoNotifications delivered %d bytes, want 0", len(resp))
	}
}

// TestTableNotificationIsNotSentForAnUnchangedFolder pins the baseline contract at
// both ends. The poll that runs at the end of the opening Execute takes the
// table's first baseline and must report nothing, so the Get*Table response
// carries its own 10-byte reply and no RopNotify. A later wake-up over a folder
// that has not moved since must likewise report nothing. Without both, a client
// would be told to re-read rows it is holding for the first time.
func TestTableNotificationIsNotSentForAnUnchangedFolder(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	st := sess.get(logonH).store
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox),
		[]byte("From: a@test\r\nSubject: x\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append: %v", err)
	}

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h = sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	// RopId(1) + HandleIndex(1) + ReturnValue(4) + RowCount(4).
	const getTableReplyLen = 10
	openResp, _ := sess.Dispatch(buildGetContentsTableFlags(0, 1, 0), []uint32{h[1], 0xFFFFFFFF})
	if len(openResp) != getTableReplyLen {
		t.Errorf("GetContentsTable response = %d bytes, want %d (the reply alone, no RopNotify)",
			len(openResp), getTableReplyLen)
	}

	if resp, _ := sess.Dispatch(nil, nil); len(resp) != 0 {
		t.Errorf("an unchanged folder delivered %d bytes, want 0", len(resp))
	}
}
