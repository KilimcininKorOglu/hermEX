package rop

import (
	"bytes"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// userDNFor builds the reversible address-book DN the GAL hands out for a mailbox,
// the form a client echoes back in RopLogon's Essdn (the final cn= is the SMTP).
func userDNFor(smtp string) string {
	return "/o=hermex/ou=hermex/cn=Recipients/cn=" + smtp
}

// delegateLogonRequest builds a RopLogon carrying an Essdn that names a (possibly
// other) target mailbox, NUL-terminated as on the wire.
func delegateLogonRequest(hindex, logonFlags uint8, essdn string) []byte {
	rb := ext.NewPush(ext.FlagUTF16)
	rb.Uint8(ropLogon)
	rb.Uint8(0)
	rb.Uint8(hindex)
	rb.Uint8(logonFlags)
	rb.Uint32(0) // OpenFlags
	rb.Uint32(0) // StoreState
	raw := append([]byte(essdn), 0)
	rb.Uint16(uint16(len(raw)))
	rb.Raw(raw)
	return rb.Bytes()
}

// setDelegateList designates the given addresses as delegates of the mailbox by
// opening its store directly (the provisioning side).
func setDelegateList(t *testing.T, dir string, list []string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetDelegates(list); err != nil {
		t.Fatal(err)
	}
}

// readLogonResponseFlags pulls a RopLogon response's ReturnValue and (on success)
// its ResponseFlags byte, skipping the LogonFlags and the 13 special-folder EIDs.
func readLogonResponseFlags(t *testing.T, resp []byte) (ec uint32, flags uint8) {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	if ec = mustU32(t, p, "ec"); ec != ecSuccess {
		return ec, 0
	}
	mustU8(t, p, "LogonFlags")
	for i := 0; i < 13; i++ {
		mustU64(t, p, "FolderId")
	}
	return ec, mustU8(t, p, "ResponseFlags")
}

// TestDelegateLogonViaDelegateList drives the real logon path: a caller designated on
// the target's delegate list opens the target mailbox in delegate mode (Reserved plus
// the send-as bit — list membership confers send-on-behalf — but not the owner right)
// and is registered for per-folder authorization.
func TestDelegateLogonViaDelegateList(t *testing.T) {
	callerDir := t.TempDir()
	targetDir := t.TempDir()
	setDelegateList(t, targetDir, []string{"delegate@hermex.test"})
	accounts := directory.StaticAccounts{
		"boss@hermex.test":     {MailboxPath: targetDir},
		"delegate@hermex.test": {MailboxPath: callerDir},
	}
	sess := NewSession(callerDir, accounts, "delegate@hermex.test")
	defer sess.Close()

	resp, h := sess.Dispatch(delegateLogonRequest(0, 0x01, userDNFor("boss@hermex.test")), []uint32{0xFFFFFFFF})
	ec, flags := readLogonResponseFlags(t, resp)
	if ec != ecSuccess {
		t.Fatalf("delegate logon ec = %#x, want success", ec)
	}
	if want := uint8(responseFlagReserved | responseFlagSendAsRight); flags != want {
		t.Errorf("delegate ResponseFlags = %#x, want %#x (reserved + send-as: on the delegate list)", flags, want)
	}
	logon := sess.get(h[0])
	if logon == nil || logon.store == nil {
		t.Fatal("delegate logon registered no store")
	}
	if caller, ok := sess.delegateCallers[logon.store]; !ok || caller != "delegate@hermex.test" {
		t.Errorf("delegate context = %q (ok=%v), want delegate@hermex.test", caller, ok)
	}
}

// TestDelegateLogonViaFolderGrant proves the other open path: a caller with a folder
// permission on the target (but not on its delegate list) may also open it.
func TestDelegateLogonViaFolderGrant(t *testing.T) {
	callerDir := t.TempDir()
	targetDir := t.TempDir()
	grantFolderPermission(t, targetDir, int64(mapi.PrivateFIDInbox), "delegate@hermex.test", mapi.RightsReviewer)
	accounts := directory.StaticAccounts{
		"boss@hermex.test":     {MailboxPath: targetDir},
		"delegate@hermex.test": {MailboxPath: callerDir},
	}
	sess := NewSession(callerDir, accounts, "delegate@hermex.test")
	defer sess.Close()

	resp, h := sess.Dispatch(delegateLogonRequest(0, 0x01, userDNFor("boss@hermex.test")), []uint32{0xFFFFFFFF})
	if ec, _ := readLogonResponseFlags(t, resp); ec != ecSuccess {
		t.Fatalf("folder-grant delegate logon ec = %#x, want success", ec)
	}
	if logon := sess.get(h[0]); logon == nil || sess.delegateCallers[logon.store] != "delegate@hermex.test" {
		t.Error("folder-grant delegate not registered")
	}
}

// TestDelegateLogonDeniedWithoutAccess proves the gate: a caller with neither a
// delegate designation nor any folder grant on the target is refused at logon.
func TestDelegateLogonDeniedWithoutAccess(t *testing.T) {
	callerDir := t.TempDir()
	targetDir := t.TempDir()
	accounts := directory.StaticAccounts{
		"boss@hermex.test":     {MailboxPath: targetDir},
		"stranger@hermex.test": {MailboxPath: callerDir},
	}
	sess := NewSession(callerDir, accounts, "stranger@hermex.test")
	defer sess.Close()

	resp, _ := sess.Dispatch(delegateLogonRequest(0, 0x01, userDNFor("boss@hermex.test")), []uint32{0xFFFFFFFF})
	if ec := ropResultEC(t, resp); ec != ecAccessDenied {
		t.Errorf("stranger delegate logon ec = %#x, want AccessDenied", ec)
	}
	if len(sess.delegateCallers) != 0 {
		t.Errorf("a denied logon registered a delegate context: %v", sess.delegateCallers)
	}
}

// TestOwnerLogonWithSelfEssdn confirms an owner logon naming its own mailbox in the
// Essdn stays an owner logon (full response flags, no delegate registration) — the
// path an alias login must not misroute.
func TestOwnerLogonWithSelfEssdn(t *testing.T) {
	dir := t.TempDir()
	accounts := directory.StaticAccounts{"owner@hermex.test": {MailboxPath: dir}}
	sess := NewSession(dir, accounts, "owner@hermex.test")
	defer sess.Close()

	resp, _ := sess.Dispatch(delegateLogonRequest(0, 0x01, userDNFor("owner@hermex.test")), []uint32{0xFFFFFFFF})
	ec, flags := readLogonResponseFlags(t, resp)
	if ec != ecSuccess {
		t.Fatalf("owner self-Essdn logon ec = %#x, want success", ec)
	}
	if flags != ownerResponseFlags {
		t.Errorf("owner ResponseFlags = %#x, want %#x", flags, ownerResponseFlags)
	}
	if len(sess.delegateCallers) != 0 {
		t.Errorf("owner logon registered a delegate context: %v", sess.delegateCallers)
	}
}

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

// buildEmptyFolder, buildCreateFolder, buildDeleteFolder, buildMoveFolder, and
// buildCopyFolder build the folder-mutation requests the exhaustiveness test drives
// (the other write ROPs already have builders in their own test files).
func buildEmptyFolder(inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropEmptyFolder)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(0) // WantAsynchronous
	b.Uint8(0) // WantDeleteAssociated
	return b.Bytes()
}

func buildCreateFolder(inIdx uint8, name string) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropCreateFolder)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(1) // OutputHandleIndex
	b.Uint8(1) // FolderType
	b.Uint8(1) // UseUnicode
	b.Uint8(0) // OpenExisting
	b.Uint32(0)
	b.Unicode(name)
	b.Unicode("")
	return b.Bytes()
}

func buildDeleteFolder(inIdx uint8, folderEID uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropDeleteFolder)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(0) // DeleteFlags
	b.Uint64(folderEID)
	return b.Bytes()
}

func buildMoveFolder(inIdx, destIdx uint8, folderEID uint64, name string) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropMoveFolder)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(destIdx)
	b.Uint8(0) // WantAsynchronous
	b.Uint8(1) // UseUnicode
	b.Uint64(folderEID)
	b.Unicode(name)
	return b.Bytes()
}

func buildCopyFolder(inIdx, destIdx uint8, folderEID uint64, name string) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropCopyFolder)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(destIdx)
	b.Uint8(0) // WantAsynchronous
	b.Uint8(0) // WantRecursive
	b.Uint8(1) // UseUnicode
	b.Uint64(folderEID)
	b.Unicode(name)
	return b.Bytes()
}

// TestDelegateWritesDeniedForReviewer is the exhaustiveness keystone: a read-only
// (Reviewer) delegate — granted ReadAny|Visible, no write bits — must be refused
// EVERY mutating ROP it can reach with its read handles. A handler that forgot its
// write gate returns ecSuccess here and fails the test.
func TestDelegateWritesDeniedForReviewer(t *testing.T) {
	dir := t.TempDir()
	const delegate = "delegate@hermex.test"
	msgID := seedFolderMessage(t, dir, int64(mapi.PrivateFIDInbox), "X")
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDInbox), delegate, mapi.RightsReviewer)

	sess, logonH := delegateLogon(t, dir, delegate)
	defer sess.Close()

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))

	// A Reviewer may open the folder and read a message; those handles then feed the
	// write attempts below.
	_, h := sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	inboxH := h[1]
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, msgEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	if inboxH == 0xFFFFFFFF || msgH == 0xFFFFFFFF {
		t.Fatal("Reviewer could not open the folder/message it holds read rights to")
	}

	props := mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "edited"}}
	tags := []mapi.PropTag{mapi.PrSubject}
	writes := []struct {
		name string
		req  []byte
		h    []uint32
	}{
		{"CreateMessage", buildCreateMessage(0, 1, inboxEID), []uint32{inboxH, 0xFFFFFFFF}},
		{"DeleteMessages", buildDeleteMessages(0, msgEID), []uint32{inboxH}},
		{"EmptyFolder", buildEmptyFolder(0), []uint32{inboxH}},
		{"CreateFolder", buildCreateFolder(0, "Sub"), []uint32{inboxH, 0xFFFFFFFF}},
		{"DeleteFolder", buildDeleteFolder(0, inboxEID), []uint32{inboxH}},
		{"ModifyPermissions", buildModifyPermissions(t, 0, 0, nil), []uint32{inboxH}},
		{"ModifyRules", buildModifyRules(t, 0, 0, nil), []uint32{inboxH}},
		{"SetMessageStatus", buildSetMessageStatus(0, msgEID, 0, 0xFFFFFFFF), []uint32{inboxH}},
		{"SyncOpenCollector", buildOpenCollector(0, 1, 1), []uint32{inboxH, 0xFFFFFFFF}},
		{"SetProperties", buildSetProperties(0, props), []uint32{msgH}},
		{"DeleteProperties", buildDeletePropsOp(ropDeleteProperties, 0, tags), []uint32{msgH}},
		{"SaveChangesMessage", buildSaveChangesMessage(0, 0), []uint32{msgH}},
		{"SetMessageReadFlag", buildSetMessageReadFlag(0, 0, rfDefault), []uint32{msgH}},
		{"CreateAttachment", buildCreateAttachment(0, 1), []uint32{msgH, 0xFFFFFFFF}},
		{"OpenStream(write)", buildOpenStream(0, 1, uint32(mapi.PrBody), streamWriteMode), []uint32{msgH, 0xFFFFFFFF}},
		{"MoveCopyMessages", buildMoveCopyMessages(0, 1, 1, msgEID), []uint32{inboxH, inboxH}},
		{"MoveFolder", buildMoveFolder(0, 1, inboxEID, "x"), []uint32{inboxH, inboxH}},
		{"CopyFolder", buildCopyFolder(0, 1, inboxEID, "x"), []uint32{inboxH, inboxH}},
		{"CopyTo", buildCopyTo(0, 1, 0, nil), []uint32{msgH, msgH}},
		{"CopyProperties", buildCopyProperties(0, 1, 0, tags), []uint32{msgH, msgH}},
		{"SetReceiveFolder", buildSetReceiveFolder(0, inboxEID, "IPM.Note"), []uint32{logonH}},
	}
	for _, w := range writes {
		resp, _ := sess.Dispatch(w.req, w.h)
		if ec := ropResultEC(t, resp); ec != ecAccessDenied {
			t.Errorf("%s ec = %#x, want AccessDenied (read-only delegate)", w.name, ec)
		}
	}
}

// TestDelegateEditorWritesAndSendBoundary proves the gates are rights-aware, not a
// blanket: an Editor-equivalent delegate (granted owner rights on the folder) performs
// the writes its grant covers — including a move/copy WITHIN the mailbox once it holds
// the two-sided rights (ReadAny on the source, Create on the destination) — yet
// send-on-behalf (Submit/TransportSend) stays refused regardless of folder rights.
func TestDelegateEditorWritesAndSendBoundary(t *testing.T) {
	dir := t.TempDir()
	const delegate = "delegate@hermex.test"
	msgID := seedFolderMessage(t, dir, int64(mapi.PrivateFIDInbox), "X")
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDInbox), delegate, mapi.RightsOwner)

	sess, logonH := delegateLogon(t, dir, delegate)
	defer sess.Close()

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))

	_, h := sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	inboxH := h[1]
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, msgEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]

	// Granted writes succeed: create a message, edit the opened one.
	createResp, ch := sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{inboxH, 0xFFFFFFFF})
	if ec := ropResultEC(t, createResp); ec != ecSuccess {
		t.Fatalf("Editor CreateMessage ec = %#x, want success", ec)
	}
	newMsgH := ch[1]
	if ec := ropResultEC(t, mustDispatch(sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "ok"}}), msgH, 0xFFFFFFFF)); ec != ecSuccess {
		t.Errorf("Editor SetProperties ec = %#x, want success", ec)
	}

	// A move/copy WITHIN the delegated mailbox is permitted once the two-sided rights
	// are held: owner rights on the Inbox cover both the source ReadAny and the
	// destination Create, so an in-mailbox copy succeeds (Inc 2 refused it outright).
	if ec := ropResultEC(t, mustDispatchN(sess, buildMoveCopyMessages(0, 1, 1, msgEID), []uint32{inboxH, inboxH})); ec != ecSuccess {
		t.Errorf("Editor in-mailbox MoveCopyMessages ec = %#x, want success (two-sided rights held)", ec)
	}

	// Send-on-behalf stays denied even with full folder rights: submitting and
	// transport-sending from another's mailbox await the send-on-behalf increment.
	boundary := []struct {
		name string
		req  []byte
		h    []uint32
	}{
		{"SubmitMessage", buildSubmitMessage(0), []uint32{newMsgH}},
		{"TransportSend", buildTransportHeaderOnly(ropTransportSend, 0), []uint32{newMsgH}},
	}
	for _, b := range boundary {
		if ec := ropResultEC(t, mustDispatchN(sess, b.req, b.h)); ec != ecAccessDenied {
			t.Errorf("%s ec = %#x, want AccessDenied (send-on-behalf not yet permitted to a delegate)", b.name, ec)
		}
	}
}

// TestDelegateMoveCopyCrossMailboxUnsupported proves the cross-store guard: a delegate
// holding two logons — their own mailbox and one they fully control as a delegate —
// may not move/copy ACROSS the two mailboxes even with full rights on both sides. The
// copy runs single-store and the well-known folder ids collide across mailboxes, so a
// cross-mailbox move/copy would file into the wrong store; it is refused NotSupported.
func TestDelegateMoveCopyCrossMailboxUnsupported(t *testing.T) {
	ownDir := t.TempDir()
	bossDir := t.TempDir()
	const delegate = "delegate@hermex.test"
	grantFolderPermission(t, bossDir, int64(mapi.PrivateFIDInbox), delegate, mapi.RightsOwner)
	msgID := seedFolderMessage(t, ownDir, int64(mapi.PrivateFIDInbox), "MINE")
	accounts := directory.StaticAccounts{
		"boss@hermex.test": {MailboxPath: bossDir},
		delegate:           {MailboxPath: ownDir},
	}
	sess := NewSession(ownDir, accounts, delegate)
	defer sess.Close()

	// Logon 1: the delegate's own mailbox (its self-Essdn keeps it an owner logon);
	// logon 2: the boss's mailbox (delegate mode, opened via the full grant above).
	_, h1 := sess.Dispatch(delegateLogonRequest(0, 0x01, userDNFor(delegate)), []uint32{0xFFFFFFFF})
	ownLogonH := h1[0]
	_, h2 := sess.Dispatch(delegateLogonRequest(0, 0x01, userDNFor("boss@hermex.test")), []uint32{0xFFFFFFFF})
	bossLogonH := h2[0]

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, oh := sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{ownLogonH, 0xFFFFFFFF})
	ownInboxH := oh[1]
	_, bh := sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{bossLogonH, 0xFFFFFFFF})
	bossInboxH := bh[1]
	if ownInboxH == 0xFFFFFFFF || bossInboxH == 0xFFFFFFFF {
		t.Fatal("could not open both mailboxes' Inbox")
	}

	// Copy from the delegate's own Inbox into the boss's Inbox: both sides are fully
	// permitted, but the operation crosses physical mailboxes and is unsupported.
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))
	resp := mustDispatchN(sess, buildMoveCopyMessages(0, 1, 1, msgEID), []uint32{ownInboxH, bossInboxH})
	if ec := ropResultEC(t, resp); ec != ecNotSupported {
		t.Errorf("cross-mailbox MoveCopyMessages ec = %#x, want NotSupported", ec)
	}
}

// mustDispatchN runs one ROP with an arbitrary handle array and returns the response.
func mustDispatchN(sess *Session, req []byte, handles []uint32) []byte {
	resp, _ := sess.Dispatch(req, handles)
	return resp
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

// TestDelegateSendsOnBehalf drives the real send-on-behalf path end to end: a delegate
// on the boss's delegate list composes a message in the boss's mailbox and submits it.
// The delivered message must go out From the boss (the represented principal) with the
// delegate named as its Sender — the "<delegate> on behalf of <boss>" wire form — the
// Sent Items copy must land in the boss's mailbox (not the delegate's), and the logon
// must have advertised the send-as right.
func TestDelegateSendsOnBehalf(t *testing.T) {
	bossDir, delegateDir, aliceDir := t.TempDir(), t.TempDir(), t.TempDir()
	const delegate = "delegate@hermex.test"
	const boss = "boss@hermex.test"
	// On the delegate list (the send-on-behalf grant) and holding Create on the boss's
	// Drafts so the compose chain runs.
	setDelegateList(t, bossDir, []string{delegate})
	grantFolderPermission(t, bossDir, int64(mapi.PrivateFIDDraft), delegate, mapi.RightsOwner)
	accounts := directory.StaticAccounts{
		boss:                {MailboxPath: bossDir},
		delegate:            {MailboxPath: delegateDir},
		"alice@hermex.test": {MailboxPath: aliceDir},
	}
	sess := NewSession(delegateDir, accounts, delegate)
	defer sess.Close()

	logonResp, h := sess.Dispatch(delegateLogonRequest(0, 0x01, userDNFor(boss)), []uint32{0xFFFFFFFF})
	ec, flags := readLogonResponseFlags(t, logonResp)
	if ec != ecSuccess {
		t.Fatalf("delegate logon ec = %#x, want success", ec)
	}
	if flags&responseFlagSendAsRight == 0 {
		t.Errorf("delegate logon ResponseFlags = %#x, want the send-as bit set (on the list)", flags)
	}
	logonH := h[0]

	// Compose To alice in the boss's Drafts, save, and submit.
	draftsEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))
	_, ch := sess.Dispatch(buildCreateMessage(0, 1, draftsEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := ch[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "ONBEHALF"},
		{Tag: mapi.PrBody, Value: "sent by the secretary"},
	}), []uint32{msgH})
	toRow := buildSMTPRecipientRow(0, mapi.RecipTo, "alice@hermex.test", "Alice")
	sess.Dispatch(buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, toRow), []uint32{msgH})
	sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})

	if ec := ropResultEC(t, mustDispatchN(sess, buildSubmitMessage(0), []uint32{msgH})); ec != ecSuccess {
		t.Fatalf("delegate SubmitMessage ec = %#x, want success", ec)
	}

	// The delivered copy carries From: boss and Sender: delegate.
	aliceRaw := firstInboxRaw(t, aliceDir)
	if !headerNames(aliceRaw, "From:", boss) {
		t.Errorf("delivered From does not name the principal %q:\n%s", boss, aliceRaw)
	}
	if !headerNames(aliceRaw, "Sender:", delegate) {
		t.Errorf("delivered Sender does not name the delegate %q:\n%s", delegate, aliceRaw)
	}

	// The Sent Items copy files in the principal's mailbox, not the delegate's.
	if n := sentItemsCount(t, bossDir); n != 1 {
		t.Errorf("boss Sent Items = %d, want 1 (send-on-behalf files in the principal's mailbox)", n)
	}
	if n := sentItemsCount(t, delegateDir); n != 0 {
		t.Errorf("delegate Sent Items = %d, want 0", n)
	}
}

// TestDelegateFolderGrantCannotSend proves the send grant is distinct from folder
// rights: a delegate admitted by a folder grant alone (full owner rights on the boss's
// Drafts, but not on the delegate list) may compose, but its submit is refused — and
// its logon never advertised the send-as right.
func TestDelegateFolderGrantCannotSend(t *testing.T) {
	bossDir, delegateDir, aliceDir := t.TempDir(), t.TempDir(), t.TempDir()
	const delegate = "delegate@hermex.test"
	const boss = "boss@hermex.test"
	grantFolderPermission(t, bossDir, int64(mapi.PrivateFIDDraft), delegate, mapi.RightsOwner)
	accounts := directory.StaticAccounts{
		boss:                {MailboxPath: bossDir},
		delegate:            {MailboxPath: delegateDir},
		"alice@hermex.test": {MailboxPath: aliceDir},
	}
	sess := NewSession(delegateDir, accounts, delegate)
	defer sess.Close()

	logonResp, h := sess.Dispatch(delegateLogonRequest(0, 0x01, userDNFor(boss)), []uint32{0xFFFFFFFF})
	ec, flags := readLogonResponseFlags(t, logonResp)
	if ec != ecSuccess {
		t.Fatalf("folder-grant delegate logon ec = %#x, want success", ec)
	}
	if flags&responseFlagSendAsRight != 0 {
		t.Errorf("folder-grant delegate logon advertised the send-as bit (%#x); it is not on the list", flags)
	}
	logonH := h[0]

	draftsEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))
	_, ch := sess.Dispatch(buildCreateMessage(0, 1, draftsEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := ch[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "NOPE"}}), []uint32{msgH})
	toRow := buildSMTPRecipientRow(0, mapi.RecipTo, "alice@hermex.test", "Alice")
	sess.Dispatch(buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, toRow), []uint32{msgH})
	sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})

	if ec := ropResultEC(t, mustDispatchN(sess, buildSubmitMessage(0), []uint32{msgH})); ec != ecAccessDenied {
		t.Errorf("folder-grant delegate SubmitMessage ec = %#x, want AccessDenied (not on the delegate list)", ec)
	}
	// Nothing was sent: the boss's Sent Items stays empty.
	if n := sentItemsCount(t, bossDir); n != 0 {
		t.Errorf("boss Sent Items = %d, want 0 (submit was refused)", n)
	}
}

// headerNames reports whether the message header block carries a header line with the
// given prefix (e.g. "From:" / "Sender:") that names addr.
func headerNames(raw []byte, prefix, addr string) bool {
	for line := range bytes.SplitSeq(raw, []byte("\r\n")) {
		if len(line) == 0 {
			break // end of header block
		}
		if bytes.HasPrefix(line, []byte(prefix)) && bytes.Contains(line, []byte(addr)) {
			return true
		}
	}
	return false
}

// sentItemsCount returns the number of messages in a mailbox's Sent Items folder.
func sentItemsCount(t *testing.T, dir string) int {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}
