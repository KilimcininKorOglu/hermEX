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

func TestCreateFolderSuccess(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDIPMSubtree))), []uint32{logonH, 0xFFFFFFFF})
	storeH := h[1]

	// RopCreateFolder body: ohindex, FolderType, UseUnicode, OpenExisting, Reserved, name, comment
	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(0)  // ohindex
	body.Uint8(0)  // FolderType
	body.Uint8(1)  // UseUnicode
	body.Uint8(0)  // OpenExisting
	body.Uint32(0) // Reserved
	body.Unicode("TestSub")
	body.Unicode("")

	resp, _ := sess.Dispatch(toROPRequest(ropCreateFolder, 0, body.Bytes()), []uint32{storeH})
	if ec := readEC(t, resp, ropCreateFolder); ec != ecSuccess {
		t.Fatalf("CreateFolder ec = %#x", ec)
	}
}

// TestHardDeleteMessagesAndSubfolders clears a folder's messages AND removes its
// subfolders in one ROP: the inbox holds a message and a subfolder, and after the
// ROP both are gone (the message recoverable in the dumpster, the subfolder dropped
// with its subtree), with PartialCompletion=0.
func TestHardDeleteMessagesAndSubfolders(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "TOPMSG")
	inboxFID := int64(mapi.PrivateFIDInbox)
	store, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	subID, err := store.CreateFolder(&inboxFID, "SubToNuke")
	if err != nil {
		store.Close()
		t.Fatalf("store.CreateFolder: %v", err)
	}
	store.Close()

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), []uint32{logonH, 0xFFFFFFFF})
	inboxH := h[1]
	st := sess.get(inboxH).store

	// Precondition: the inbox has a message and the subfolder.
	if msgs, _ := st.ListMessages(inboxFID); len(msgs) == 0 {
		t.Fatal("expected a message in Inbox")
	}
	if kids, _ := childFolders(st, inboxFID); len(kids) == 0 {
		t.Fatal("expected a subfolder under Inbox")
	}

	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(0) // WantAsynchronous
	body.Uint8(0) // WantDeleteAssociated
	resp, _ := sess.Dispatch(toROPRequest(ropHardDelMsgsAndSubfolders, 0, body.Bytes()), []uint32{inboxH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropHardDelMsgsAndSubfolders {
		t.Fatalf("HardDeleteMessagesAndSubfolders RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("HardDeleteMessagesAndSubfolders ReturnValue = %#x", ec)
	}
	if pc := mustU8(t, p, "partialCompletion"); pc != 0 {
		t.Errorf("PartialCompletion = %d, want 0", pc)
	}

	// The folder's messages are cleared (to the dumpster).
	if after, _ := st.ListMessages(inboxFID); len(after) != 0 {
		t.Errorf("%d messages remain after HardDeleteMessagesAndSubfolders", len(after))
	}
	if dump, _ := st.ListSoftDeleted(inboxFID); len(dump) != 1 {
		t.Errorf("dumpster = %d, want 1 (cleared message recoverable)", len(dump))
	}
	// The subfolder is removed.
	kids, _ := childFolders(st, inboxFID)
	for _, k := range kids {
		if k.ID == subID {
			t.Errorf("subfolder %d was not deleted", subID)
		}
	}
}

func TestEmptyFolderDeletesMessages(t *testing.T) {
	dir := t.TempDir()
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
		pre.Close()
		t.Fatalf("store.CreateFolder: %v", err)
	}
	pre.Close()

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), []uint32{logonH, 0xFFFFFFFF})
	inboxH := h[1]

	store := sess.get(inboxH).store
	before, _ := store.ListMessages(mapi.PrivateFIDInbox)
	if len(before) == 0 {
		t.Fatal("expected messages in Inbox")
	}
	if kids, _ := childFolders(store, inboxFID); len(kids) == 0 {
		t.Fatal("expected a subfolder under Inbox")
	}

	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(0)
	body.Uint8(0)

	resp, _ := sess.Dispatch(toROPRequest(ropEmptyFolder, 0, body.Bytes()), []uint32{inboxH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropEmptyFolder {
		t.Fatalf("EmptyFolder RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("EmptyFolder ec = %#x", ec)
	}
	if pc := mustU8(t, p, "partialCompletion"); pc != 0 {
		t.Errorf("PartialCompletion = %d, want 0", pc)
	}

	after, _ := store.ListMessages(mapi.PrivateFIDInbox)
	if len(after) != 0 {
		t.Errorf("%d messages remain after EmptyFolder", len(after))
	}
	// The emptied messages went to the dumpster, recoverable, not purged.
	if dump, _ := store.ListSoftDeleted(mapi.PrivateFIDInbox); len(dump) != 2 {
		t.Errorf("dumpster = %d, want 2 after EmptyFolder (recoverable)", len(dump))
	}
	// The subfolder is removed too.
	kids, _ := childFolders(store, inboxFID)
	for _, k := range kids {
		if k.ID == subID {
			t.Errorf("subfolder %d was not deleted by EmptyFolder", subID)
		}
	}
}

func TestHardDeleteMessagesToDumpster(t *testing.T) {
	dir := t.TempDir()
	midA := seedInboxMessage(t, dir, "A")
	msgB := seedInboxMessage(t, dir, "B")
	midC := seedInboxMessage(t, dir, "C")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), []uint32{logonH, 0xFFFFFFFF})
	inboxH := h[1]

	store := sess.get(inboxH).store
	blob := make([]byte, 16)
	put64 := func(b []byte, v int64) {
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7] =
			byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
			byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56)
	}
	put64(blob[0:8], int64(mapi.MakeEIDEx(1, uint64(midA)))) // message ids on the wire are EIDs
	put64(blob[8:16], int64(mapi.MakeEIDEx(1, uint64(msgB))))

	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(0)
	body.Uint8(0)
	body.BinShort(blob)

	resp, _ := sess.Dispatch(toROPRequest(ropHardDeleteMessages, 0, body.Bytes()), []uint32{inboxH})
	if ec := readEC(t, resp, ropHardDeleteMessages); ec != ecSuccess {
		t.Fatalf("HardDeleteMessages ec = %#x", ec)
	}
	// A and B leave the live inbox but go to the dumpster (recoverable); C stays.
	live, _ := store.ListMessages(mapi.PrivateFIDInbox)
	if len(live) != 1 {
		t.Errorf("live inbox = %d messages, want 1 (only C)", len(live))
	}
	if dump, _ := store.ListSoftDeleted(mapi.PrivateFIDInbox); len(dump) != 2 {
		t.Errorf("dumpster = %d, want 2 (A and B recoverable)", len(dump))
	}
	if _, err := store.OpenMessage(midC); err != nil {
		t.Error("midC was deleted unexpectedly")
	}
}

func TestDeleteFolder(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDIPMSubtree))), []uint32{logonH, 0xFFFFFFFF})
	storeH := h[1]

	store := sess.get(storeH).store
	subFID, err := store.CreateFolder(nil, "SubToDelete")
	if err != nil {
		t.Fatalf("store.CreateFolder: %v", err)
	}

	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(0)
	body.Uint64(uint64(mapi.MakeEIDEx(1, uint64(subFID)))) // FolderId as an EID, like a real client

	resp, _ := sess.Dispatch(toROPRequest(ropDeleteFolder, 0, body.Bytes()), []uint32{storeH})
	if ec := readEC(t, resp, ropDeleteFolder); ec != ecSuccess {
		t.Fatalf("DeleteFolder ec = %#x", ec)
	}
	seen := false
	all, _ := store.ListFolders()
	for _, f := range all {
		if f.ID == subFID {
			seen = true
		}
	}
	if seen {
		t.Error("folder still listed after DeleteFolder")
	}
}

func TestMoveFolder(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDIPMSubtree))), []uint32{logonH, 0xFFFFFFFF})
	storeH := h[1]

	store := sess.get(storeH).store
	subFID, err := store.CreateFolder(nil, "SubToMove")
	if err != nil {
		t.Fatalf("store.CreateFolder sub: %v", err)
	}
	_, err = store.CreateFolder(nil, "Destination")
	if err != nil {
		t.Fatalf("store.CreateFolder dest: %v", err)
	}

	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(0)
	body.Uint8(0)
	body.Uint8(1)
	body.Uint64(uint64(mapi.MakeEIDEx(1, uint64(subFID)))) // FolderId as an EID, like a real client
	body.Unicode("RenamedSub")

	resp, _ := sess.Dispatch(toROPRequest(ropMoveFolder, 0, body.Bytes()), []uint32{storeH})
	if ec := readEC(t, resp, ropMoveFolder); ec != ecSuccess {
		t.Fatalf("MoveFolder ec = %#x", ec)
	}
	props, _ := store.GetFolderProperties(subFID, mapi.PrDisplayName)
	if v, ok := props.Get(mapi.PrDisplayName); !ok || v.(string) != "RenamedSub" {
		t.Errorf("display name = %v, want RenamedSub", v)
	}
}

// TestCopyFolderRecursiveAndCycle drives RopCopyFolder over the wire: a folder
// with a message is copied under a destination folder (the copy appears there),
// and copying a folder into its own subtree is refused with MAPI_E_FOLDER_CYCLE.
func TestCopyFolderRecursiveAndCycle(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
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
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDIPMSubtree))), []uint32{logonH, 0xFFFFFFFF})
	ipmH := h[1]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(dst)))), []uint32{logonH, 0xFFFFFFFF})
	dstH := h[1]

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
	resp, _ := sess.Dispatch(toROPRequest(ropCopyFolder, 0, copyBody(1, src, "Copy")), []uint32{ipmH, dstH})
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
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(sub)))), []uint32{logonH, 0xFFFFFFFF})
	subH := h[1]
	resp2, _ := sess.Dispatch(toROPRequest(ropCopyFolder, 0, copyBody(1, src, "Loop")), []uint32{ipmH, subH})
	if ec := readEC(t, resp2, ropCopyFolder); ec != ecFolderCycle {
		t.Errorf("cycle CopyFolder ec = %#x, want ecFolderCycle (%#x)", ec, ecFolderCycle)
	}
}

// folderNamedUnder reports whether a folder with the given display name exists
// directly under parentID.
func folderNamedUnder(t *testing.T, store *objectstore.Store, parentID int64, name string) bool {
	t.Helper()
	folders, err := store.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		if f.DisplayName == name && f.ParentID != nil && *f.ParentID == parentID {
			return true
		}
	}
	return false
}

func TestSetSearchCriteriaReturnsNotSupported(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), []uint32{logonH, 0xFFFFFFFF})
	inboxH := h[1]

	// SetSearchCriteria body: RestrictionDataSize (u16) + RestrictionData + FolderIds (EID_ARRAY) + SearchFlags (u32)
	body := ext.NewPush(ext.FlagUTF16)
	body.Uint16(0) // RestrictionDataSize (no restriction)
	body.EIDs(nil) // FolderIds (empty EID_ARRAY)
	body.Uint32(1) // SearchFlags (RESTART_SEARCH)

	resp, _ := sess.Dispatch(toROPRequest(ropSetSearchCriteria, 0, body.Bytes()), []uint32{inboxH})
	if ec := readEC(t, resp, ropSetSearchCriteria); ec != ecNotSupported {
		t.Errorf("SetSearchCriteria ec = %#x, want ecNotSupported", ec)
	}
}

func TestGetSearchCriteriaReturnsNotSupported(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), []uint32{logonH, 0xFFFFFFFF})
	inboxH := h[1]

	// GetSearchCriteria body: useUnicode, includeRestriction, includeFolders (three u8)
	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(1)
	body.Uint8(1)
	body.Uint8(1)

	resp, _ := sess.Dispatch(toROPRequest(ropGetSearchCriteria, 0, body.Bytes()), []uint32{inboxH})
	if ec := readEC(t, resp, ropGetSearchCriteria); ec != ecNotSupported {
		t.Errorf("GetSearchCriteria ec = %#x, want ecNotSupported", ec)
	}
}

// TestSearchCriteriaBatchAlignment verifies the request body is fully consumed so a
// following ROP in the same batch parses from the correct offset.
func TestSearchCriteriaBatchAlignment(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), []uint32{logonH, 0xFFFFFFFF})
	inboxH := h[1]

	// Build a batch: SetSearchCriteria followed by GetSearchCriteria.
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSetSearchCriteria)
	b.Uint8(0)
	b.Uint8(0)
	b.Uint16(0) // RestrictionDataSize
	b.EIDs(nil) // FolderIds
	b.Uint32(1) // SearchFlags
	b.Uint8(ropGetSearchCriteria)
	b.Uint8(0)
	b.Uint8(0)
	b.Uint8(1) // useUnicode
	b.Uint8(1) // includeRestriction
	b.Uint8(1) // includeFolders

	resp, _ := sess.Dispatch(b.Bytes(), []uint32{inboxH})
	p := ext.NewPull(resp, ext.FlagUTF16)

	// First response: SetSearchCriteria.
	if id := mustU8(t, p, "RopId"); id != ropSetSearchCriteria {
		t.Fatalf("first RopId = %#x, want SetSearchCriteria", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotSupported {
		t.Errorf("SetSearchCriteria ec = %#x, want ecNotSupported", ec)
	}
	// Second response: GetSearchCriteria — proves the parser stayed aligned.
	if id := mustU8(t, p, "RopId"); id != ropGetSearchCriteria {
		t.Fatalf("second RopId = %#x, want GetSearchCriteria (parser misaligned)", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotSupported {
		t.Errorf("GetSearchCriteria ec = %#x, want ecNotSupported", ec)
	}
}
