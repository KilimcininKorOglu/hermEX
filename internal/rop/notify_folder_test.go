package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// TestWholeStoreFolderBaselineSuppressesPreExisting pins the load-bearing
// baseline-at-registration invariant for the folder hierarchy: a whole-store
// subscription registered on a mailbox that already has its default folder tree
// reports no spurious folder events on the first poll, and a zero-mask
// whole-store subscription emits nothing even after folder mutations.
func TestWholeStoreFolderBaselineSuppressesPreExisting(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	st := sess.get(logonH).store
	const ntypes = uint8(fnevObjectCreated | fnevObjectModified | fnevObjectDeleted)
	h := dispatchOK(t, sess, buildRegisterNotification(0, 1, ntypes, 1, 0, 0), ropRegisterNotification, "RegisterNotification", logonH, 0xFFFFFFFF)
	requireHandles(t, "RegisterNotification", h[1])

	zeroH := dispatchOK(t, sess, buildRegisterNotification(0, 1, 0, 1, 0, 0), ropRegisterNotification, "RegisterNotification(zero mask)", logonH, 0xFFFFFFFF)[1]
	requireHandles(t, "RegisterNotification(zero mask)", zeroH)

	if resp := dispatchOn(sess, nil); len(resp) != 0 {
		t.Fatalf("first poll after whole-store registration drained %d bytes, want 0 (the existing folder tree must be baselined, not reported)", len(resp))
	}

	// Hostile/empty mask: a zero-mask subscription stays silent when a new folder appears.
	maskID, err := st.CreateFolder(nil, "MaskIgnored")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	resp := dispatchOn(sess, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)
	nextNotify(t, p, h[1], uint16(fnevObjectCreated), "masked folder-created")
	wantU64(t, p, "FolderId", uint64(mapi.MakeEIDEx(1, uint64(maskID))))
	wantU64(t, p, "ParentId", uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDIPMSubtree))))
	wantU16(t, p, "proptags", 0)
	wantDrained(t, p, "zero-mask subscription emitted nothing")
}

// TestWholeStoreFolderCreated drives a folder appearing after the baseline.
func TestWholeStoreFolderCreated(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	st := sess.get(logonH).store

	const ntypes = uint8(fnevObjectCreated)
	subH := dispatchOK(t, sess, buildRegisterNotification(0, 1, ntypes, 1, 0, 0), ropRegisterNotification, "RegisterNotification", logonH, 0xFFFFFFFF)[1]

	newID, err := st.CreateFolder(nil, "Project X")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}

	resp := dispatchOn(sess, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)
	nextNotify(t, p, subH, uint16(fnevObjectCreated), "folder-created")
	if fid := mustU64(t, p, "FolderId"); fid != uint64(mapi.MakeEIDEx(1, uint64(newID))) {
		t.Errorf("FolderId = %#x, want the new folder %#x", fid, uint64(mapi.MakeEIDEx(1, uint64(newID))))
	}
	if pid := mustU64(t, p, "ParentId"); pid != uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDIPMSubtree))) {
		t.Errorf("ParentId = %#x, want the IPM subtree %#x", pid, uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDIPMSubtree))))
	}
	if cnt := mustU16(t, p, "proptag count"); cnt != 0 {
		t.Errorf("proptag count = %d, want 0 (the create carries no changed-property set)", cnt)
	}
	wantDrained(t, p, "folder-created RopNotify")
}

// TestWholeStoreFolderDeleted drives a folder vanishing after the baseline.
func TestWholeStoreFolderDeleted(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	st := sess.get(logonH).store

	delID, err := st.CreateFolder(nil, "Doomed")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	const ntypes = uint8(fnevObjectDeleted)
	subH := dispatchOK(t, sess, buildRegisterNotification(0, 1, ntypes, 1, 0, 0), ropRegisterNotification, "RegisterNotification", logonH, 0xFFFFFFFF)[1]

	if err := st.DeleteFolder(delID); err != nil {
		t.Fatalf("delete folder: %v", err)
	}

	resp := dispatchOn(sess, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)
	nextNotify(t, p, subH, uint16(fnevObjectDeleted), "folder-deleted")
	if fid := mustU64(t, p, "FolderId"); fid != uint64(mapi.MakeEIDEx(1, uint64(delID))) {
		t.Errorf("FolderId = %#x, want the deleted folder %#x", fid, uint64(mapi.MakeEIDEx(1, uint64(delID))))
	}
	if pid := mustU64(t, p, "ParentId"); pid != uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDIPMSubtree))) {
		t.Errorf("ParentId = %#x, want the IPM subtree %#x", pid, uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDIPMSubtree))))
	}
	wantDrained(t, p, "folder-deleted RopNotify")
}

// TestWholeStoreFolderModifiedCounts proves a folder's count change surfaces as a
// folder-modified carrying NF_HAS_TOTAL/NF_HAS_UNREAD.
func TestWholeStoreFolderModifiedCounts(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	st := sess.get(logonH).store

	const ntypes = uint8(fnevObjectModified)
	subH := dispatchOK(t, sess, buildRegisterNotification(0, 1, ntypes, 1, 0, 0), ropRegisterNotification, "RegisterNotification", logonH, 0xFFFFFFFF)[1]

	inbox := int64(mapi.PrivateFIDInbox)
	if _, err := st.AppendMessage(inbox, []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append: %v", err)
	}

	resp := dispatchOn(sess, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)
	nextNotify(t, p, subH, uint16(fnevObjectModified|nfHasTotal|nfHasUnread), "folder-modified")
	if fid := mustU64(t, p, "FolderId"); fid != uint64(mapi.MakeEIDEx(1, uint64(inbox))) {
		t.Errorf("FolderId = %#x, want Inbox %#x", fid, uint64(mapi.MakeEIDEx(1, uint64(inbox))))
	}
	if cnt := mustU16(t, p, "proptag count"); cnt != 0 {
		t.Errorf("proptag count = %d, want 0", cnt)
	}
	if total := mustU32(t, p, "total"); total != 1 {
		t.Errorf("total = %d, want 1 (one message now in the Inbox)", total)
	}
	if unread := mustU32(t, p, "unread"); unread != 1 {
		t.Errorf("unread = %d, want 1 (the delivered message is unread)", unread)
	}
	wantDrained(t, p, "folder-modified RopNotify")
}

// TestWholeStoreMessageEmitsFolderModifiedAndMessageCreated pins the real Outlook flow.
func TestWholeStoreMessageEmitsFolderModifiedAndMessageCreated(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	st := sess.get(logonH).store

	const ntypes = uint8(fnevObjectCreated | fnevObjectModified)
	subH := dispatchOK(t, sess, buildRegisterNotification(0, 1, ntypes, 1, 0, 0), ropRegisterNotification, "RegisterNotification", logonH, 0xFFFFFFFF)[1]

	inbox := int64(mapi.PrivateFIDInbox)
	inboxEID := uint64(mapi.MakeEIDEx(1, uint64(inbox)))
	info, err := st.AppendMessage(inbox, []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	resp := dispatchOn(sess, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)

	// First: the folder-modified for the Inbox (the hierarchy pass precedes the sweep).
	nextNotify(t, p, subH, uint16(fnevObjectModified|nfHasTotal|nfHasUnread), "folder-modified")
	wantU64(t, p, "folder-modified FolderId (Inbox)", inboxEID)
	mustU16(t, p, "folder-modified proptag count")
	wantU32(t, p, "folder-modified total", 1)
	mustU32(t, p, "folder-modified unread")

	// Second: the message-created for the delivered message.
	nextNotify(t, p, subH, uint16(fnevObjectCreated|nfByMessage), "message-created")
	wantU64(t, p, "message-created FolderId (Inbox)", inboxEID)
	wantU64(t, p, "message-created MessageId (the delivered message)", uint64(mapi.MakeEIDEx(1, uint64(info.ID))))
	mustU16(t, p, "message-created proptag count")
	wantDrained(t, p, "the two notifications (want exactly folder-modified + message-created)")
}

// nextNotify reads the next RopNotify header out of the response stream and
// asserts the subscription it belongs to and the event flags it carries.
func nextNotify(t *testing.T, p *ext.Pull, subH uint32, flags uint16, what string) {
	t.Helper()
	if id := mustU8(t, p, what+" RopId"); id != ropNotify {
		t.Fatalf("%s RopId = %#x, want RopNotify", what, id)
	}
	wantU32(t, p, what+" NotificationHandle", subH)
	mustU8(t, p, what+" LogonId")
	wantU16(t, p, what+" NotificationFlags", flags)
}
