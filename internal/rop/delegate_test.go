package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// grantFolderPermission stores a delegate's rights on one folder of a mailbox by
// opening the store directly (the provisioning side), so a later logon resolves
// them. rights is a union of mapi.Frights* bits.
func grantFolderPermission(t *testing.T, dir string, folderID int64, username string, rights uint32) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.ModifyPermissions(folderID, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: username, Rights: rights},
	}); err != nil {
		t.Fatal(err)
	}
}

// seedFolderMessage delivers one message into an arbitrary folder and returns its
// objectstore id (seedInboxMessage generalized off the Inbox).
func seedFolderMessage(t *testing.T, dir string, folderID int64, subject string) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := []byte("From: sender@hermex.test\r\nTo: alice@hermex.test\r\n" +
		"Subject: " + subject + "\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\nhello body\r\n")
	info, err := st.AppendMessage(folderID, raw, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// ropResultEC reads a ROP response's ReturnValue. The 3-field prefix (RopId,
// HandleIndex, ec) is shared by every ROP response and by the generic error form,
// so it reads both a success and a denial.
func ropResultEC(t *testing.T, resp []byte) uint32 {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "HandleIndex")
	return mustU32(t, p, "ec")
}

// delegateLogon opens dir as a session and registers the logon store as a delegate
// of caller, so its ops authorize against caller's folder permissions. It returns
// the session and the logon handle.
func delegateLogon(t *testing.T, dir, caller string) (*Session, uint32) {
	t.Helper()
	sess := NewSession(dir, nil, "")
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	if logonH == 0xFFFFFFFF {
		t.Fatal("logon handle not set")
	}
	logon := sess.get(logonH)
	if logon == nil || logon.store == nil {
		t.Fatal("logon object has no store")
	}
	sess.delegateCallers[logon.store] = caller
	return sess, logonH
}

// TestDelegateReadGatesByFolderRights proves the read chokepoints enforce a
// delegate's per-folder rights: ReadAny opens the folder, its contents table and its
// messages; Visible-only opens the folder but denies its contents (reading items
// needs ReadAny, which opening did not require); no grant denies the open outright.
func TestDelegateReadGatesByFolderRights(t *testing.T) {
	dir := t.TempDir()
	const delegate = "delegate@hermex.test"

	inboxMsg := seedFolderMessage(t, dir, int64(mapi.PrivateFIDInbox), "READABLE")
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDInbox), delegate, mapi.RightsReviewer)     // ReadAny|Visible
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDSentItems), delegate, mapi.FrightsVisible) // Visible only
	// DeletedItems: no grant at all.

	sess, logonH := delegateLogon(t, dir, delegate)
	defer sess.Close()

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	sentEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDSentItems))
	deletedEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDeletedItems))

	// Inbox: ReadAny ⇒ open, contents, and message all succeed.
	openInbox, h := sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	if ec := ropResultEC(t, openInbox); ec != ecSuccess {
		t.Fatalf("OpenFolder(Inbox) ec = %#x, want success (Visible held)", ec)
	}
	inboxH := h[1]
	if ec := ropResultEC(t, mustDispatch(sess, buildGetContentsTable(0, 1), inboxH, 0xFFFFFFFF)); ec != ecSuccess {
		t.Errorf("GetContentsTable(Inbox) ec = %#x, want success (ReadAny held)", ec)
	}
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(inboxMsg)))
	if ec := ropResultEC(t, mustDispatch(sess, buildOpenMessage(0, 1, inboxEID, msgEID), logonH, 0xFFFFFFFF)); ec != ecSuccess {
		t.Errorf("OpenMessage(Inbox msg) ec = %#x, want success (ReadAny held)", ec)
	}

	// SentItems: Visible only ⇒ open succeeds, contents denied.
	openSent, h := sess.Dispatch(buildOpenFolder(0, 1, sentEID), []uint32{logonH, 0xFFFFFFFF})
	if ec := ropResultEC(t, openSent); ec != ecSuccess {
		t.Fatalf("OpenFolder(SentItems) ec = %#x, want success (Visible held)", ec)
	}
	sentH := h[1]
	if ec := ropResultEC(t, mustDispatch(sess, buildGetContentsTable(0, 1), sentH, 0xFFFFFFFF)); ec != ecAccessDenied {
		t.Errorf("GetContentsTable(SentItems) ec = %#x, want AccessDenied (no ReadAny)", ec)
	}

	// DeletedItems: no grant ⇒ open denied.
	if ec := ropResultEC(t, mustDispatch(sess, buildOpenFolder(0, 1, deletedEID), logonH, 0xFFFFFFFF)); ec != ecAccessDenied {
		t.Errorf("OpenFolder(DeletedItems) ec = %#x, want AccessDenied (no Visible)", ec)
	}
}

// TestDelegateMessageGateUsesRealParent is the security keystone: a delegate must
// not read a message by naming a folder they CAN read in the request while the
// message really lives in one they cannot. The gate resolves the message's true
// parent folder from the store and ignores the (informational) wire FolderId.
func TestDelegateMessageGateUsesRealParent(t *testing.T) {
	dir := t.TempDir()
	const delegate = "delegate@hermex.test"

	// The message lives in SentItems (Visible only — no ReadAny); the delegate has
	// ReadAny on the Inbox.
	sentMsg := seedFolderMessage(t, dir, int64(mapi.PrivateFIDSentItems), "PRIVATE")
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDInbox), delegate, mapi.RightsReviewer)
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDSentItems), delegate, mapi.FrightsVisible)

	sess, logonH := delegateLogon(t, dir, delegate)
	defer sess.Close()

	// Open the message off the logon, but LIE in the wire FolderId: claim it is in
	// the Inbox (which the delegate can read). The gate must still deny, because the
	// message's real parent (SentItems) lacks ReadAny.
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(sentMsg)))
	resp := mustDispatch(sess, buildOpenMessage(0, 1, inboxEID, msgEID), logonH, 0xFFFFFFFF)
	if ec := ropResultEC(t, resp); ec != ecAccessDenied {
		t.Errorf("OpenMessage with spoofed FolderId ec = %#x, want AccessDenied (real parent has no ReadAny)", ec)
	}
}

// TestOwnerBypassUnaffectedByGates proves the gates are inert for an owner: the same
// session WITHOUT a delegate registration opens a folder that carries no permission
// row at all (DeletedItems) and reads its messages — the owner short-circuit grants
// full access without consulting the permission table.
func TestOwnerBypassUnaffectedByGates(t *testing.T) {
	dir := t.TempDir()
	delMsg := seedFolderMessage(t, dir, int64(mapi.PrivateFIDDeletedItems), "OWNED")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	// No delegateCallers entry: this is an owner logon.

	deletedEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDeletedItems))
	open, h := sess.Dispatch(buildOpenFolder(0, 1, deletedEID), []uint32{logonH, 0xFFFFFFFF})
	if ec := ropResultEC(t, open); ec != ecSuccess {
		t.Fatalf("owner OpenFolder(DeletedItems) ec = %#x, want success (no permission row, owner unrestricted)", ec)
	}
	folderH := h[1]
	if ec := ropResultEC(t, mustDispatch(sess, buildGetContentsTable(0, 1), folderH, 0xFFFFFFFF)); ec != ecSuccess {
		t.Errorf("owner GetContentsTable(DeletedItems) ec = %#x, want success", ec)
	}
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(delMsg)))
	if ec := ropResultEC(t, mustDispatch(sess, buildOpenMessage(0, 1, deletedEID, msgEID), logonH, 0xFFFFFFFF)); ec != ecSuccess {
		t.Errorf("owner OpenMessage(DeletedItems msg) ec = %#x, want success", ec)
	}
}
