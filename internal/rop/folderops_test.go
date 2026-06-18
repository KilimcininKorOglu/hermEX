package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
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
	mustU8(t, p, "ropId")
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

func TestEmptyFolderDeletesMessages(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "M1")
	seedInboxMessage(t, dir, "M2")
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

	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(0)
	body.Uint8(0)

	resp, _ := sess.Dispatch(toROPRequest(ropEmptyFolder, 0, body.Bytes()), []uint32{inboxH})
	if ec := readEC(t, resp, ropEmptyFolder); ec != ecSuccess {
		t.Fatalf("EmptyFolder ec = %#x", ec)
	}

	after, _ := store.ListMessages(mapi.PrivateFIDInbox)
	if len(after) != 0 {
		t.Errorf("%d messages remain after EmptyFolder", len(after))
	}
}

func TestHardDeleteMessagesRemovesCorrect(t *testing.T) {
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
	put64(blob[0:8], midA)
	put64(blob[8:16], msgB)

	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(0)
	body.Uint8(0)
	body.BinShort(blob)

	resp, _ := sess.Dispatch(toROPRequest(ropHardDeleteMessages, 0, body.Bytes()), []uint32{inboxH})
	if ec := readEC(t, resp, ropHardDeleteMessages); ec != ecSuccess {
		t.Fatalf("HardDeleteMessages ec = %#x", ec)
	}
	if _, err := store.OpenMessage(midA); err == nil {
		t.Error("midA not deleted")
	}
	if _, err := store.OpenMessage(msgB); err == nil {
		t.Error("msgB not deleted")
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
	body.Uint64(uint64(subFID))

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
	body.Uint64(uint64(subFID))
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

func TestCopyFolderReturnsNotSupported(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()

	body := ext.NewPush(ext.FlagUTF16)
	body.Uint8(0)
	body.Uint8(0)
	body.Uint8(0)
	body.Uint8(1)
	body.Uint64(0)
	body.Unicode("")

	resp, _ := sess.Dispatch(toROPRequest(ropCopyFolder, 0, body.Bytes()), []uint32{0})
	if ec := readEC(t, resp, ropCopyFolder); ec != ecNotSupported {
		t.Errorf("CopyFolder ec = %#x, want ecNotSupported", ec)
	}
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
