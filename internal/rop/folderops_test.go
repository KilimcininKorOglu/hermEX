package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

func toROPRequest(ropID uint8, hindex uint8, body []byte) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropID)
	b.Uint8(0)
	b.Uint8(hindex)
	b.Raw(body)
	return b.Bytes()
}

func readEC(t *testing.T, resp []byte, wantID uint8) uint32 {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "ropId"); id != wantID {
		t.Fatalf("RopId = %#x, want %#x", id, wantID)
	}
	mustU8(t, p, "hindex")
	return mustU32(t, p, "ec")
}

// logonOpen logs on as the owner and opens folder fid, checking both steps, and
// returns the logon and folder handles.
func logonOpen(t *testing.T, sess *Session, fid uint64) (logonH, folderH uint32) {
	t.Helper()
	logonH = dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	h := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, fid))), ropOpenFolder, "OpenFolder", logonH, 0xFFFFFFFF)
	return logonH, h[1]
}

func TestCreateFolderSuccess(t *testing.T) {
	dir := provisionedDir(t)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, storeH := logonOpen(t, sess, mapi.PrivateFIDIPMSubtree)

	resp := dispatchOn(sess, toROPRequest(ropCreateFolder, 0, createFolderBody(1, "TestSub", false)), storeH, 0xFFFFFFFF)
	if ec := readEC(t, resp, ropCreateFolder); ec != ecSuccess {
		t.Fatalf("CreateFolder ec = %#x", ec)
	}
}

// createFolderBody is a RopCreateFolder request body opening the new folder at
// output index 1: OutputHandleIndex, FolderType, UseUnicode, OpenExisting,
// Reserved (each one byte), then the name and an empty comment.
func createFolderBody(folderType uint8, name string, openExisting bool) []byte {
	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(1)
	body.Uint8(folderType)
	body.Uint8(1)
	if openExisting {
		body.Uint8(1)
	} else {
		body.Uint8(0)
	}
	body.Uint8(0)
	body.Unicode(name)
	body.Unicode("")
	return body.Bytes()
}

// openInboxForCreate logs on and opens the Inbox, returning the session and
// the Inbox handle.
func openInboxForCreate(t *testing.T) (*Session, uint32) {
	t.Helper()
	sess := NewSession(provisionedDir(t), nil, "")
	t.Cleanup(sess.Close)
	_, inboxH := logonOpen(t, sess, mapi.PrivateFIDInbox)
	return sess, inboxH
}

// TestCreateFolderOpensTheNewFolder proves RopCreateFolder is framed as a client
// sends it (a one-byte Reserved field), answers with FolderId and a zero
// IsExistingFolder and nothing after it, and opens the new folder at the output
// handle, so a property read on that handle names the folder.
func TestCreateFolderOpensTheNewFolder(t *testing.T) {
	sess, inboxH := openInboxForCreate(t)
	resp, h := sess.Dispatch(toROPRequest(ropCreateFolder, 0, createFolderBody(1, "Projects", false)), []uint32{inboxH, 0xFFFFFFFF})
	if len(resp) != 6+8+1 {
		t.Fatalf("CreateFolder response is %d bytes (%x), want 15", len(resp), resp)
	}
	p := ropOK(t, resp, ropCreateFolder, "CreateFolder")
	fid := int64(mapi.EID(mustU64(t, p, "FolderId")).GCValue())
	wantU8(t, p, "IsExistingFolder", 0)
	if h[1] == 0xFFFFFFFF {
		t.Fatal("CreateFolder left the output handle unset")
	}
	if got := sess.get(h[1]); got == nil || got.kind != kindFolder || got.folderID != fid {
		t.Fatalf("output handle holds %+v, want folder %d", got, fid)
	}
	got := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, []mapi.PropTag{mapi.PrDisplayName}), h[1])
	gp := ropOK(t, got, ropGetPropertiesSpecific, "GetPropertiesSpecific on the new folder")
	wantU8(t, gp, "row flag", 0)
	if name, err := gp.Unicode(); err != nil || name != "Projects" {
		t.Fatalf("display name = %q, %v", name, err)
	}
}

// TestCreateFolderNameCollision proves a name already taken under the parent is
// refused with ecDuplicateName, and that OpenExisting opens that folder instead.
func TestCreateFolderNameCollision(t *testing.T) {
	sess, inboxH := openInboxForCreate(t)
	first := dispatchOn(sess, toROPRequest(ropCreateFolder, 0, createFolderBody(1, "Twice", false)), inboxH, 0xFFFFFFFF)
	p := ropOK(t, first, ropCreateFolder, "first CreateFolder")
	fid := mustU64(t, p, "FolderId")

	dup := dispatchOn(sess, toROPRequest(ropCreateFolder, 0, createFolderBody(1, "Twice", false)), inboxH, 0xFFFFFFFF)
	if ec := readEC(t, dup, ropCreateFolder); ec != ecDuplicateName {
		t.Fatalf("duplicate CreateFolder ec = %#x, want ecDuplicateName", ec)
	}
	if n := foldersNamedUnder(t, sess.get(inboxH).store, int64(mapi.PrivateFIDInbox), "Twice"); n != 1 {
		t.Errorf("after the refused duplicate the Inbox holds %d folders named Twice, want 1", n)
	}
	again, h := sess.Dispatch(toROPRequest(ropCreateFolder, 0, createFolderBody(1, "Twice", true)), []uint32{inboxH, 0xFFFFFFFF})
	p = ropOK(t, again, ropCreateFolder, "CreateFolder with OpenExisting")
	if got := mustU64(t, p, "FolderId"); got != fid {
		t.Fatalf("OpenExisting returned folder %#x, want %#x", got, fid)
	}
	if o := sess.get(h[1]); o == nil || o.kind != kindFolder {
		t.Fatal("OpenExisting did not open the folder at the output handle")
	}
}

// TestCreateFolderRefusesOtherTypes proves a type other than generic or search is
// an invalid parameter.
func TestCreateFolderRefusesOtherTypes(t *testing.T) {
	sess, inboxH := openInboxForCreate(t)
	for ft, want := range map[uint8]uint32{0: ecInvalidParam, 7: ecInvalidParam} {
		resp := dispatchOn(sess, toROPRequest(ropCreateFolder, 0, createFolderBody(ft, "Typed", false)), inboxH, 0xFFFFFFFF)
		if ec := readEC(t, resp, ropCreateFolder); ec != want {
			t.Errorf("FolderType %d: ec = %#x, want %#x", ft, ec, want)
		}
	}
	if folderNamedUnder(t, sess.get(inboxH).store, int64(mapi.PrivateFIDInbox), "Typed") {
		t.Error("a refused FolderType still created the folder")
	}
}

// TestDelegateOwnsTheFolderTheyCreate proves a delegate who may only create
// subfolders under the Inbox holds owner rights on the folder they create, so they
// can read its contents, which the Inbox grant alone would not allow.
func TestDelegateOwnsTheFolderTheyCreate(t *testing.T) {
	dir := provisionedDir(t)
	const delegate = "delegate@hermex.test"
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDInbox), delegate, mapi.FrightsVisible|mapi.FrightsCreateSubfolder)
	sess, logonH := delegateLogon(t, dir, delegate)
	defer sess.Close()
	h := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), ropOpenFolder, "delegate OpenFolder(Inbox)", logonH, 0xFFFFFFFF)
	resp, h := sess.Dispatch(toROPRequest(ropCreateFolder, 0, createFolderBody(1, "Mine", false)), []uint32{h[1], 0xFFFFFFFF})
	p := ropOK(t, resp, ropCreateFolder, "delegate CreateFolder")
	fid := int64(mapi.EID(mustU64(t, p, "FolderId")).GCValue())

	if ec := ropResultEC(t, mustDispatch(sess, buildGetContentsTable(0, 1), h[1], 0xFFFFFFFF)); ec != ecSuccess {
		t.Errorf("GetContentsTable on the new folder ec = %#x, want success", ec)
	}
	rights, err := sess.get(logonH).store.ResolvePermission(fid, delegate)
	if err != nil {
		t.Fatal(err)
	}
	if rights != creatorRights {
		t.Errorf("delegate rights on the new folder = %#x, want %#x", rights, creatorRights)
	}
}

// TestOwnerCreatedFolderGrantsNobody proves an owner's new folder carries no
// member row: the owner needs none, and no one else is granted anything.
func TestOwnerCreatedFolderGrantsNobody(t *testing.T) {
	sess, inboxH := openInboxForCreate(t)
	resp, h := sess.Dispatch(toROPRequest(ropCreateFolder, 0, createFolderBody(1, "Plain", false)), []uint32{inboxH, 0xFFFFFFFF})
	p := ropOK(t, resp, ropCreateFolder, "CreateFolder")
	fid := int64(mapi.EID(mustU64(t, p, "FolderId")).GCValue())
	perms, err := sess.get(h[1]).store.ListPermissions(fid)
	if err != nil {
		t.Fatal(err)
	}
	if len(perms) != 0 {
		t.Errorf("owner-created folder has permission rows %+v, want none", perms)
	}
}

// TestCreateFolderKeepsTheComment proves the request's comment is stored on the
// new folder as PidTagComment.
func TestCreateFolderKeepsTheComment(t *testing.T) {
	sess, inboxH := openInboxForCreate(t)
	body := ext.NewPush(ext.FlagUTF16)
	body.Raw([]byte{1, 1, 1, 0, 0})
	body.Unicode("Noted")
	body.Unicode("folder for notes")
	resp, h := sess.Dispatch(toROPRequest(ropCreateFolder, 0, body.Bytes()), []uint32{inboxH, 0xFFFFFFFF})
	ropOK(t, resp, ropCreateFolder, "CreateFolder with a comment")
	got := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, []mapi.PropTag{mapi.PrComment}), h[1])
	gp := ropOK(t, got, ropGetPropertiesSpecific, "GetPropertiesSpecific(PrComment)")
	wantU8(t, gp, "row flag", 0)
	if c, err := gp.Unicode(); err != nil || c != "folder for notes" {
		t.Fatalf("comment = %q, %v", c, err)
	}
}

// TestHardDeleteMessagesAndSubfolders clears a folder's messages AND removes its
// subfolders in one ROP: the inbox holds a message and a subfolder, and after the
// ROP both are gone (the message recoverable in the dumpster, the subfolder dropped
// with its subtree), with PartialCompletion=0.
func TestHardDeleteMessagesAndSubfolders(t *testing.T) {
	dir := provisionedDir(t)
	seedInboxMessage(t, dir, "TOPMSG")
	inboxFID := int64(mapi.PrivateFIDInbox)
	store, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	subID, err := store.CreateFolder(&inboxFID, "SubToNuke")
	if err != nil {
		mustClose(t, store)
		t.Fatalf("store.CreateFolder: %v", err)
	}
	mustClose(t, store)

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, inboxH := logonOpen(t, sess, mapi.PrivateFIDInbox)
	st := sess.get(inboxH).store

	// Precondition: the inbox has a message and the subfolder.
	assertInboxHasContent(t, st, inboxFID)

	clear := dispatchOn(sess, toROPRequest(ropHardDelMsgsAndSubfolders, 0, clearFolderRequest()), inboxH)
	p := ropOK(t, clear, ropHardDelMsgsAndSubfolders, "HardDeleteMessagesAndSubfolders")
	wantU8(t, p, "HardDeleteMessagesAndSubfolders PartialCompletion", 0)

	assertFolderCleared(t, st, inboxFID, subID, 1, "HardDeleteMessagesAndSubfolders")
}

// clearFolderRequest builds the body RopEmptyFolder and
// RopHardDeleteMessagesAndSubfolders share: WantAsynchronous, then
// WantDeleteAssociated, both off.
func clearFolderRequest() []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(0) // WantAsynchronous
	b.Uint8(0) // WantDeleteAssociated
	return b.Bytes()
}

// assertInboxHasContent is the precondition both clear-the-folder tests need:
// something to remove, so an empty result afterwards means something happened.
func assertInboxHasContent(t *testing.T, st *objectstore.Store, inboxFID int64) {
	t.Helper()
	if msgs := mustListMessages(t, st, inboxFID); len(msgs) == 0 {
		t.Fatal("expected messages in Inbox")
	}
	if kids := mustChildFolders(t, st, inboxFID); len(kids) == 0 {
		t.Fatal("expected a subfolder under Inbox")
	}
}

// assertFolderCleared checks what a clear-the-folder ROP left behind: no live
// messages, the removed ones recoverable in the dumpster rather than purged, and
// the subfolder gone.
func assertFolderCleared(t *testing.T, st *objectstore.Store, inboxFID, subID int64, wantDumpster int, what string) {
	t.Helper()
	if after := mustListMessages(t, st, inboxFID); len(after) != 0 {
		t.Errorf("%d messages remain after %s", len(after), what)
	}
	if dump := mustListSoftDeleted(t, st, inboxFID); len(dump) != wantDumpster {
		t.Errorf("dumpster = %d, want %d after %s (cleared messages stay recoverable)", len(dump), wantDumpster, what)
	}
	for _, k := range mustChildFolders(t, st, inboxFID) {
		if k.ID == subID {
			t.Errorf("subfolder %d was not deleted by %s", subID, what)
		}
	}
}

func TestEmptyFolderDeletesMessages(t *testing.T) {
	dir := provisionedDir(t)
	seedInboxMessage(t, dir, "M1")
	seedInboxMessage(t, dir, "M2")
	inboxFID := int64(mapi.PrivateFIDInbox)
	// EmptyFolder spans messages AND subfolders, so seed a subfolder to clear too.
	pre, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	subID, err := pre.CreateFolder(&inboxFID, "SubToEmpty")
	if err != nil {
		mustClose(t, pre)
		t.Fatalf("store.CreateFolder: %v", err)
	}
	mustClose(t, pre)

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, inboxH := logonOpen(t, sess, mapi.PrivateFIDInbox)

	store := sess.get(inboxH).store
	assertInboxHasContent(t, store, inboxFID)

	resp := dispatchOn(sess, toROPRequest(ropEmptyFolder, 0, clearFolderRequest()), inboxH)
	p := ropOK(t, resp, ropEmptyFolder, "EmptyFolder")
	wantU8(t, p, "EmptyFolder PartialCompletion", 0)

	assertFolderCleared(t, store, inboxFID, subID, 2, "EmptyFolder")
}

func TestHardDeleteMessagesToDumpster(t *testing.T) {
	dir := provisionedDir(t)
	midA := seedInboxMessage(t, dir, "A")
	msgB := seedInboxMessage(t, dir, "B")
	midC := seedInboxMessage(t, dir, "C")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, inboxH := logonOpen(t, sess, mapi.PrivateFIDInbox)

	store := sess.get(inboxH).store
	// A message of another folder: the request names it through the Inbox
	// handle, which may delete only the Inbox's own messages.
	other, err := store.CreateFolder(nil, "Other")
	mustNil(t, err)
	foreign, err := store.AppendMessage(other, []byte("From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: F\r\n\r\nbody\r\n"), time.Now(), 0)
	mustNil(t, err)

	// MessageIds is a 16-bit count of 8-byte EIDs, as RopDeleteMessages carries it.
	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(0)
	body.Uint8(0)
	mustBuild(body.Uint64ArrayShort([]uint64{
		uint64(mapi.MakeEIDEx(1, uint64(midA))),
		uint64(mapi.MakeEIDEx(1, uint64(msgB))),
		uint64(mapi.MakeEIDEx(1, uint64(foreign.ID))),
	}))

	resp := dispatchOn(sess, toROPRequest(ropHardDeleteMessages, 0, body.Bytes()), inboxH)
	p := ropOK(t, resp, ropHardDeleteMessages, "HardDeleteMessages")
	// [MS-OXCROPS] 2.2.4.11.2: PartialCompletion is nonzero when the server
	// could not delete every message the request named.
	wantU8(t, p, "PartialCompletion with a foreign message id", 1)
	// A and B leave the live inbox but go to the dumpster (recoverable); C stays.
	live := mustListMessages(t, store, mapi.PrivateFIDInbox)
	if len(live) != 1 || live[0].ID != midC {
		t.Errorf("live inbox = %+v, want only message %d (C)", live, midC)
	}
	if dump := mustListSoftDeleted(t, store, mapi.PrivateFIDInbox); len(dump) != 2 {
		t.Errorf("dumpster = %d, want 2 (A and B recoverable)", len(dump))
	}
	if kept := mustListMessages(t, store, other); len(kept) != 1 || kept[0].ID != foreign.ID {
		t.Errorf("the other folder holds %+v after the request, want its message %d untouched", kept, foreign.ID)
	}
}

func TestDeleteFolder(t *testing.T) {
	dir := provisionedDir(t)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, storeH := logonOpen(t, sess, mapi.PrivateFIDIPMSubtree)

	store := sess.get(storeH).store
	subFID, err := store.CreateFolder(nil, "SubToDelete")
	if err != nil {
		t.Fatalf("store.CreateFolder: %v", err)
	}

	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(0)
	body.Uint64(uint64(mapi.MakeEIDEx(1, uint64(subFID)))) // FolderId as an EID, like a real client

	resp := dispatchOn(sess, toROPRequest(ropDeleteFolder, 0, body.Bytes()), storeH)
	if ec := readEC(t, resp, ropDeleteFolder); ec != ecSuccess {
		t.Fatalf("DeleteFolder ec = %#x", ec)
	}
	all, err := store.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range all {
		if f.ID == subFID {
			t.Error("folder still listed after DeleteFolder")
		}
	}
}

// mustListMessages lists a folder's live messages and fails the test on a read
// error, so an emptiness check never passes on a failed read.
func mustListMessages(t *testing.T, st *objectstore.Store, folderID int64) []objectstore.MessageInfo {
	t.Helper()
	msgs, err := st.ListMessages(folderID)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

// mustListSoftDeleted lists a folder's recoverable messages and fails the test on
// a read error.
func mustListSoftDeleted(t *testing.T, st *objectstore.Store, folderID int64) []objectstore.SoftDeletedMessage {
	t.Helper()
	dump, err := st.ListSoftDeleted(folderID)
	if err != nil {
		t.Fatal(err)
	}
	return dump
}

// mustChildFolders lists a folder's direct subfolders and fails the test on a
// read error.
func mustChildFolders(t *testing.T, st *objectstore.Store, parentID int64) []objectstore.FolderInfo {
	t.Helper()
	kids, err := childFolders(st, parentID)
	if err != nil {
		t.Fatal(err)
	}
	return kids
}

func TestMoveFolder(t *testing.T) {
	dir := provisionedDir(t)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, storeH := logonOpen(t, sess, mapi.PrivateFIDIPMSubtree)

	store := sess.get(storeH).store
	subFID, err := store.CreateFolder(nil, "SubToMove")
	if err != nil {
		t.Fatalf("store.CreateFolder sub: %v", err)
	}
	dstFID, err := store.CreateFolder(nil, "Destination")
	if err != nil {
		t.Fatalf("store.CreateFolder dest: %v", err)
	}

	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(0)
	body.Uint8(0)
	body.Uint8(1)
	body.Uint64(uint64(mapi.MakeEIDEx(1, uint64(subFID)))) // FolderId as an EID, like a real client
	body.Unicode("RenamedSub")

	resp := dispatchOn(sess, toROPRequest(ropMoveFolder, 0, body.Bytes()), storeH)
	if ec := readEC(t, resp, ropMoveFolder); ec != ecSuccess {
		t.Fatalf("MoveFolder ec = %#x", ec)
	}
	props, err := store.GetFolderProperties(subFID, mapi.PrDisplayName)
	mustNil(t, err)
	wantProp(t, props, mapi.PrDisplayName, "RenamedSub", "display name")

	// A move onto a sibling's name is refused with ecDuplicateName ([MS-OXCFOLD]
	// 3.2.5.2: sibling folders cannot have the same name), and the folder keeps
	// its name.
	body = ext.NewPush(ext.FlagUTF16)
	body.Uint8(0)
	body.Uint8(0)
	body.Uint8(1)
	body.Uint64(uint64(mapi.MakeEIDEx(1, uint64(subFID))))
	body.Unicode("Destination")
	resp = dispatchOn(sess, toROPRequest(ropMoveFolder, 0, body.Bytes()), storeH)
	if ec := readEC(t, resp, ropMoveFolder); ec != ecDuplicateName {
		t.Errorf("MoveFolder onto a sibling's name ec = %#x, want ecDuplicateName", ec)
	}
	props, err = store.GetFolderProperties(subFID, mapi.PrDisplayName)
	mustNil(t, err)
	wantProp(t, props, mapi.PrDisplayName, "RenamedSub", "display name after the refused move")
	props, err = store.GetFolderProperties(dstFID, mapi.PrDisplayName)
	mustNil(t, err)
	wantProp(t, props, mapi.PrDisplayName, "Destination", "sibling name after the refused move")
}

// TestCopyFolderRecursiveAndCycle drives RopCopyFolder over the wire: a folder
// with a message is copied under a destination folder (the copy appears there),
// and copying a folder into its own subtree is refused with MAPI_E_FOLDER_CYCLE.
func TestCopyFolderRecursiveAndCycle(t *testing.T) {
	dir := provisionedDir(t)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store
	ipm := int64(mapi.PrivateFIDIPMSubtree)

	src, err := store.CreateFolder(&ipm, "Src")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(src, []byte("From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: X\r\n\r\nbody\r\n"), time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	dst, err := store.CreateFolder(&ipm, "Dst")
	if err != nil {
		t.Fatal(err)
	}

	// Open the IPM subtree (source parent / store provider) and the destination.
	ipmH := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDIPMSubtree))), ropOpenFolder, "OpenFolder(IPM)", logonH, 0xFFFFFFFF)[1]
	dstH := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(dst)))), ropOpenFolder, "OpenFolder(Dst)", logonH, 0xFFFFFFFF)[1]

	copyBody := func(destIdx uint8, srcFID int64, name string) []byte {
		b := ext.NewPush(ext.FlagUTF16)
		b.Uint8(destIdx)                                    // DestHandleIndex
		b.Uint8(0)                                          // WantAsynchronous
		b.Uint8(1)                                          // WantRecursive
		b.Uint8(1)                                          // UseUnicode
		b.Uint64(uint64(mapi.MakeEIDEx(1, uint64(srcFID)))) // FolderId as an EID, like a real client
		b.Unicode(name)
		return b.Bytes()
	}

	// Copy Src -> Dst as "Copy": success, and the copy appears under Dst.
	resp := dispatchOn(sess, toROPRequest(ropCopyFolder, 0, copyBody(1, src, "Copy")), ipmH, dstH)
	if ec := readEC(t, resp, ropCopyFolder); ec != ecSuccess {
		t.Fatalf("CopyFolder ec = %#x, want success", ec)
	}
	if !folderNamedUnder(t, store, dst, "Copy") {
		t.Error("copied folder \"Copy\" not found under the destination")
	}

	// Cycle: copying Src into its own subfolder is refused with ecFolderCycle.
	sub, err := store.CreateFolder(&src, "Sub")
	if err != nil {
		t.Fatal(err)
	}
	subH := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(sub)))), ropOpenFolder, "OpenFolder(Sub)", logonH, 0xFFFFFFFF)[1]
	resp2 := dispatchOn(sess, toROPRequest(ropCopyFolder, 0, copyBody(1, src, "Loop")), ipmH, subH)
	if ec := readEC(t, resp2, ropCopyFolder); ec != ecFolderCycle {
		t.Errorf("cycle CopyFolder ec = %#x, want ecFolderCycle (%#x)", ec, ecFolderCycle)
	}
	if folderNamedUnder(t, store, sub, "Loop") {
		t.Error("the refused cycle copy still created \"Loop\" inside its own subtree")
	}
}

// folderNamedUnder reports whether a folder with the given display name exists
// directly under parentID.
func folderNamedUnder(t *testing.T, store *objectstore.Store, parentID int64, name string) bool {
	t.Helper()
	return foldersNamedUnder(t, store, parentID, name) > 0
}

// foldersNamedUnder counts the folders with the given display name directly
// under parentID.
func foldersNamedUnder(t *testing.T, store *objectstore.Store, parentID int64, name string) int {
	t.Helper()
	folders, err := store.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range folders {
		if f.DisplayName == name && f.ParentID != nil && *f.ParentID == parentID {
			n++
		}
	}
	return n
}

// TestMoveFolderOntoANonFolderHandleRenamesInPlace: a destination handle that
// names no folder asks only for a rename, so the folder keeps its parent. A
// subfolder of the Inbox renamed this way must stay under the Inbox, not move to
// the top of the IPM subtree.
func TestMoveFolderOntoANonFolderHandleRenamesInPlace(t *testing.T) {
	dir := provisionedDir(t)
	inbox := int64(mapi.PrivateFIDInbox)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := st.CreateFolder(&inbox, "Nested")
	mustClose(t, st)
	if err != nil {
		t.Fatal(err)
	}
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH, inboxH := logonOpen(t, sess, mapi.PrivateFIDInbox)

	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(1) // DestHandleIndex: the logon, not a folder
	body.Uint8(0) // WantAsynchronous
	body.Uint8(1) // UseUnicode
	body.Uint64(uint64(mapi.MakeEIDEx(1, uint64(sub))))
	body.Unicode("Renamed")
	resp := dispatchOn(sess, toROPRequest(ropMoveFolder, 0, body.Bytes()), inboxH, logonH)
	if ec := readEC(t, resp, ropMoveFolder); ec != ecSuccess {
		t.Fatalf("MoveFolder ec = %#x", ec)
	}
	store := sess.get(inboxH).store
	kids, err := childFolders(store, inbox)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, k := range kids {
		found = found || k.ID == sub
	}
	if !found {
		t.Error("the renamed folder left the Inbox")
	}
	props, err := store.GetFolderProperties(sub, mapi.PrDisplayName)
	mustNil(t, err)
	wantProp(t, props, mapi.PrDisplayName, "Renamed", "display name")
}
