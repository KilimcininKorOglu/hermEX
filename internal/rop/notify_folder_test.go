package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// TestWholeStoreFolderBaselineSuppressesPreExisting pins the load-bearing
// baseline-at-registration invariant for the folder hierarchy: a whole-store
// subscription registered on a mailbox that already has its default folder tree must
// baseline that tree, so the first poll reports no spurious folder created/deleted/
// modified. Without the registration baseline the first drain would flood the client
// with every existing folder as a create — the same bug class the message baseline
// guards against.
func TestWholeStoreFolderBaselineSuppressesPreExisting(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	const ntypes = uint8(fnevObjectCreated | fnevObjectModified | fnevObjectDeleted)
	sess.Dispatch(buildRegisterNotification(0, 1, ntypes, 1, 0, 0), []uint32{logonH, 0xFFFFFFFF})

	resp, _ := sess.Dispatch(nil, nil)
	if len(resp) != 0 {
		t.Fatalf("first poll after whole-store registration drained %d bytes, want 0 (the existing folder tree must be baselined, not reported)", len(resp))
	}
}

// TestWholeStoreFolderCreated drives a folder appearing after the baseline: a
// whole-store subscriber to created events gets one folder-level RopNotify carrying
// the new folder id and its parent (the IPM subtree, since the folder is top-level),
// with nfByMessage clear so no message id rides and an empty changed-property set.
func TestWholeStoreFolderCreated(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	st := sess.get(logonH).store

	const ntypes = uint8(fnevObjectCreated)
	_, h = sess.Dispatch(buildRegisterNotification(0, 1, ntypes, 1, 0, 0), []uint32{logonH, 0xFFFFFFFF})
	subH := h[1]

	newID, err := st.CreateFolder(nil, "Project X")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}

	resp, _ := sess.Dispatch(nil, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropNotify {
		t.Fatalf("RopId = %#x, want RopNotify %#x", id, ropNotify)
	}
	if got := mustU32(t, p, "NotificationHandle"); got != subH {
		t.Errorf("NotificationHandle = %d, want the whole-store subscription handle %d", got, subH)
	}
	mustU8(t, p, "LogonId")
	if nflags := mustU16(t, p, "nflags"); nflags != uint16(fnevObjectCreated) {
		t.Errorf("nflags = %#x, want %#x (folder created, byMessage clear)", nflags, fnevObjectCreated)
	}
	if fid := mustU64(t, p, "FolderId"); fid != uint64(mapi.MakeEIDEx(1, uint64(newID))) {
		t.Errorf("FolderId = %#x, want the new folder %#x", fid, uint64(mapi.MakeEIDEx(1, uint64(newID))))
	}
	if pid := mustU64(t, p, "ParentId"); pid != uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDIPMSubtree))) {
		t.Errorf("ParentId = %#x, want the IPM subtree %#x", pid, uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDIPMSubtree))))
	}
	if cnt := mustU16(t, p, "proptag count"); cnt != 0 {
		t.Errorf("proptag count = %d, want 0 (the create carries no changed-property set)", cnt)
	}
	if p.Remaining() != 0 {
		t.Errorf("trailing bytes after the folder-created RopNotify: %d", p.Remaining())
	}
}

// TestWholeStoreFolderDeleted drives a folder vanishing after the baseline: the
// subscriber to deleted events gets a folder-level RopNotify naming the gone folder
// and the parent it was removed from. A delete carries no changed-property set.
func TestWholeStoreFolderDeleted(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	st := sess.get(logonH).store

	// The folder exists at subscribe time, so the baseline includes it.
	delID, err := st.CreateFolder(nil, "Doomed")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	const ntypes = uint8(fnevObjectDeleted)
	_, h = sess.Dispatch(buildRegisterNotification(0, 1, ntypes, 1, 0, 0), []uint32{logonH, 0xFFFFFFFF})
	subH := h[1]

	if err := st.DeleteFolder(delID); err != nil {
		t.Fatalf("delete folder: %v", err)
	}

	resp, _ := sess.Dispatch(nil, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropNotify {
		t.Fatalf("RopId = %#x, want RopNotify %#x", id, ropNotify)
	}
	if got := mustU32(t, p, "NotificationHandle"); got != subH {
		t.Errorf("NotificationHandle = %d, want %d", got, subH)
	}
	mustU8(t, p, "LogonId")
	if nflags := mustU16(t, p, "nflags"); nflags != uint16(fnevObjectDeleted) {
		t.Errorf("nflags = %#x, want %#x (folder deleted)", nflags, fnevObjectDeleted)
	}
	if fid := mustU64(t, p, "FolderId"); fid != uint64(mapi.MakeEIDEx(1, uint64(delID))) {
		t.Errorf("FolderId = %#x, want the deleted folder %#x", fid, uint64(mapi.MakeEIDEx(1, uint64(delID))))
	}
	if pid := mustU64(t, p, "ParentId"); pid != uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDIPMSubtree))) {
		t.Errorf("ParentId = %#x, want the IPM subtree %#x", pid, uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDIPMSubtree))))
	}
	if p.Remaining() != 0 {
		t.Errorf("trailing bytes after the folder-deleted RopNotify: %d", p.Remaining())
	}
}

// TestWholeStoreFolderModifiedCounts proves a folder's count change surfaces as a
// folder-modified carrying NF_HAS_TOTAL/NF_HAS_UNREAD. Subscribing to MODIFIED only
// isolates it: the new message's own create event is filtered (the subscription lacks
// the created bit), so the single drained RopNotify is the Inbox's count update — the
// event a client uses to refresh the folder tree's unread badge.
func TestWholeStoreFolderModifiedCounts(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	st := sess.get(logonH).store

	const ntypes = uint8(fnevObjectModified)
	_, h = sess.Dispatch(buildRegisterNotification(0, 1, ntypes, 1, 0, 0), []uint32{logonH, 0xFFFFFFFF})
	subH := h[1]

	inbox := int64(mapi.PrivateFIDInbox)
	if _, err := st.AppendMessage(inbox, []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append: %v", err)
	}

	resp, _ := sess.Dispatch(nil, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropNotify {
		t.Fatalf("RopId = %#x, want RopNotify %#x", id, ropNotify)
	}
	if got := mustU32(t, p, "NotificationHandle"); got != subH {
		t.Errorf("NotificationHandle = %d, want %d", got, subH)
	}
	mustU8(t, p, "LogonId")
	if nflags := mustU16(t, p, "nflags"); nflags != uint16(fnevObjectModified|nfHasTotal|nfHasUnread) {
		t.Errorf("nflags = %#x, want %#x (folder modified | hasTotal | hasUnread)", nflags, fnevObjectModified|nfHasTotal|nfHasUnread)
	}
	if fid := mustU64(t, p, "FolderId"); fid != uint64(mapi.MakeEIDEx(1, uint64(inbox))) {
		t.Errorf("FolderId = %#x, want Inbox %#x", fid, uint64(mapi.MakeEIDEx(1, uint64(inbox))))
	}
	// A folder-modified carries no parent id and no message id; the changed-property
	// set comes next, then the two counts.
	if cnt := mustU16(t, p, "proptag count"); cnt != 0 {
		t.Errorf("proptag count = %d, want 0", cnt)
	}
	if total := mustU32(t, p, "total"); total != 1 {
		t.Errorf("total = %d, want 1 (one message now in the Inbox)", total)
	}
	if unread := mustU32(t, p, "unread"); unread != 1 {
		t.Errorf("unread = %d, want 1 (the delivered message is unread)", unread)
	}
	if p.Remaining() != 0 {
		t.Errorf("trailing bytes after the folder-modified RopNotify: %d", p.Remaining())
	}
}

// TestWholeStoreMessageEmitsFolderModifiedAndMessageCreated pins the real Outlook
// flow, which the type-isolated tests above cannot: a client registers created AND
// modified together, so one message into the Inbox yields BOTH the folder-modified
// that refreshes the tree's unread badge AND the message-created for the content
// table, delivered in one drain. The hierarchy pass runs before the message sweep, so
// the folder-modified comes first; this guards against a future reorder.
func TestWholeStoreMessageEmitsFolderModifiedAndMessageCreated(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	st := sess.get(logonH).store

	const ntypes = uint8(fnevObjectCreated | fnevObjectModified)
	_, h = sess.Dispatch(buildRegisterNotification(0, 1, ntypes, 1, 0, 0), []uint32{logonH, 0xFFFFFFFF})
	subH := h[1]

	inbox := int64(mapi.PrivateFIDInbox)
	inboxEID := uint64(mapi.MakeEIDEx(1, uint64(inbox)))
	info, err := st.AppendMessage(inbox, []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	resp, _ := sess.Dispatch(nil, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)

	// First: the folder-modified for the Inbox (the hierarchy pass precedes the sweep).
	if id := mustU8(t, p, "RopId#1"); id != ropNotify {
		t.Fatalf("first RopId = %#x, want RopNotify", id)
	}
	if got := mustU32(t, p, "handle#1"); got != subH {
		t.Errorf("first NotificationHandle = %d, want %d", got, subH)
	}
	mustU8(t, p, "logon#1")
	if nf := mustU16(t, p, "nflags#1"); nf != uint16(fnevObjectModified|nfHasTotal|nfHasUnread) {
		t.Errorf("first event nflags = %#x, want folder-modified|hasTotal|hasUnread %#x", nf, fnevObjectModified|nfHasTotal|nfHasUnread)
	}
	if fid := mustU64(t, p, "folder#1"); fid != inboxEID {
		t.Errorf("first event FolderId = %#x, want Inbox %#x", fid, inboxEID)
	}
	mustU16(t, p, "proptag count#1")
	if total := mustU32(t, p, "total#1"); total != 1 {
		t.Errorf("folder-modified total = %d, want 1", total)
	}
	mustU32(t, p, "unread#1")

	// Second: the message-created for the delivered message.
	if id := mustU8(t, p, "RopId#2"); id != ropNotify {
		t.Fatalf("second RopId = %#x, want RopNotify (the message-created)", id)
	}
	mustU32(t, p, "handle#2")
	mustU8(t, p, "logon#2")
	if nf := mustU16(t, p, "nflags#2"); nf != uint16(fnevObjectCreated|nfByMessage) {
		t.Errorf("second event nflags = %#x, want message-created|byMessage %#x", nf, fnevObjectCreated|nfByMessage)
	}
	if fid := mustU64(t, p, "folder#2"); fid != inboxEID {
		t.Errorf("second event FolderId = %#x, want Inbox %#x", fid, inboxEID)
	}
	if mid := mustU64(t, p, "msg#2"); mid != uint64(mapi.MakeEIDEx(1, uint64(info.ID))) {
		t.Errorf("second event MessageId = %#x, want the delivered message %#x", mid, uint64(mapi.MakeEIDEx(1, uint64(info.ID))))
	}
	mustU16(t, p, "proptag count#2")
	if p.Remaining() != 0 {
		t.Errorf("trailing bytes after the two notifications: %d (want exactly folder-modified + message-created)", p.Remaining())
	}
}
