package rop

import (
	"reflect"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// buildOpenFolder builds a RopOpenFolder request: header (RopId, LogonId,
// InputHandleIndex) + body (OutputHandleIndex, FolderId, OpenModeFlags).
func buildOpenFolder(inIdx, outIdx uint8, folderEID uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropOpenFolder)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint64(folderEID)
	b.Uint8(0) // OpenModeFlags
	return b.Bytes()
}

// buildGetContentsTable builds a RopGetContentsTable request.
func buildGetContentsTable(inIdx, outIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetContentsTable)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(0) // TableFlags
	return b.Bytes()
}

// buildSetColumns builds a RopSetColumns request over the given column set.
func buildSetColumns(inIdx uint8, cols []mapi.PropTag) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSetColumns)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(0) // SetColumnsFlags
	_ = b.PropTags(cols)
	return b.Bytes()
}

// TestContentsTableBrowse chains Logon -> OpenFolder(Inbox) -> GetContentsTable
// -> SetColumns across separate Execute batches (the realistic flow), threading
// each server handle into the next batch's handle table, and asserts every
// response header plus that the freshly seeded Inbox reports zero rows and the
// column set is stored.
func TestContentsTableBrowse(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	// Logon -> store handle at slot 0.
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	if logonH == 0xFFFFFFFF {
		t.Fatal("logon handle not set")
	}

	// OpenFolder(Inbox): logon at input slot 0, folder output at slot 1.
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	openResp, h := sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	p := ropOK(t, openResp, ropOpenFolder, "OpenFolder")
	mustU8(t, p, "HasRules")
	mustU8(t, p, "HasGhost")
	folderH := h[1]
	if folderH == 0xFFFFFFFF {
		t.Fatal("folder handle not set")
	}
	if obj := sess.get(folderH); obj == nil || obj.kind != kindFolder || obj.folderID != int64(mapi.PrivateFIDInbox) {
		t.Fatalf("folder object wrong: %+v", obj)
	}

	// GetContentsTable: folder at input slot 0, table output at slot 1.
	gctResp, h := sess.Dispatch(buildGetContentsTable(0, 1), []uint32{folderH, 0xFFFFFFFF})
	p = ropOK(t, gctResp, ropGetContentsTable, "GetContentsTable")
	wantU32(t, p, "RowCount (freshly seeded Inbox is empty)", 0)
	tableH := h[1]
	if obj := sess.get(tableH); obj == nil || obj.kind != kindTable {
		t.Fatalf("table object not created: %+v", obj)
	}

	// SetColumns on the table (in place; no output handle).
	cols := []mapi.PropTag{mapi.PrSubject, mapi.PrSenderName, mapi.PrMessageDeliveryTime, mapi.PrMessageFlags}
	scResp, _ := sess.Dispatch(buildSetColumns(0, cols), []uint32{tableH})
	p = ropOK(t, scResp, ropSetColumns, "SetColumns")
	wantU8(t, p, "TableStatus", tableStatusComplete)
	if got := sess.get(tableH).table.columns; !reflect.DeepEqual(got, cols) {
		t.Errorf("stored columns = %v, want %v", got, cols)
	}
}

// TestOpenFolderNotFound confirms opening an entry id with no backing folder
// returns ecNotFound.
func TestOpenFolderNotFound(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	bogusEID := uint64(mapi.MakeEIDEx(1, 0x7FFFFF)) // no such folder
	resp, _ := sess.Dispatch(buildOpenFolder(0, 1, bogusEID), []uint32{logonH, 0xFFFFFFFF})
	p := ext.NewPull(resp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotFound {
		t.Errorf("OpenFolder(bogus) ReturnValue = %#x, want %#x (ecNotFound)", ec, ecNotFound)
	}
}
