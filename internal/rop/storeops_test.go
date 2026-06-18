package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// buildGetReceiveFolder builds a RopGetReceiveFolder request (MessageClass string).
func buildGetReceiveFolder(hindex uint8, class string) []byte {
	body := ext.NewPush(ext.FlagUTF16)
	body.String8(class)
	return toROPRequest(ropGetReceiveFolder, hindex, body.Bytes())
}

// buildSetReceiveFolder builds a RopSetReceiveFolder request (FolderId + MessageClass).
func buildSetReceiveFolder(hindex uint8, folderEID uint64, class string) []byte {
	body := ext.NewPush(ext.FlagUTF16)
	body.Uint64(folderEID)
	body.String8(class)
	return toROPRequest(ropSetReceiveFolder, hindex, body.Bytes())
}

// TestReceiveFolderROPs drives the four MS-OXCSTOR store ops over the wire on the
// logon handle: GetReceiveFolder resolves a class to a folder EID + explicit
// class; SetReceiveFolder maps a custom class (resolved through a sub-class) and
// rejects the un-settable IPM class and the zero-folder default removal;
// GetReceiveFolderTable returns the mappings; GetStoreState is not implemented.
func TestReceiveFolderROPs(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	sentEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDSentItems))

	// GetReceiveFolder("IPM.Note") → seeded "IPM" prefix → Inbox.
	resp, _ := sess.Dispatch(buildGetReceiveFolder(0, "IPM.Note"), []uint32{logonH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropGetReceiveFolder {
		t.Fatalf("GetReceiveFolder RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetReceiveFolder ec = %#x", ec)
	}
	if fid := mustU64(t, p, "FolderId"); fid != inboxEID {
		t.Errorf("GetReceiveFolder FolderId = %#x, want Inbox EID %#x", fid, inboxEID)
	}
	if explicit, _ := p.String8(); explicit != "IPM" {
		t.Errorf("GetReceiveFolder explicit class = %q, want IPM", explicit)
	}

	// SetReceiveFolder: map IPM.Note.Custom → Sent Items, resolve a sub-class to it.
	if ec := readEC(t, mustDispatch(sess, buildSetReceiveFolder(0, sentEID, "IPM.Note.Custom"), logonH, 0), ropSetReceiveFolder); ec != ecSuccess {
		t.Fatalf("SetReceiveFolder ec = %#x", ec)
	}
	resp, _ = sess.Dispatch(buildGetReceiveFolder(0, "IPM.Note.Custom.Sub"), []uint32{logonH})
	p = ext.NewPull(resp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	mustU32(t, p, "ec")
	if fid := mustU64(t, p, "FolderId"); fid != sentEID {
		t.Errorf("custom-class FolderId = %#x, want Sent Items EID %#x", fid, sentEID)
	}
	if explicit, _ := p.String8(); explicit != "IPM.Note.Custom" {
		t.Errorf("custom-class explicit = %q, want IPM.Note.Custom", explicit)
	}

	// IPM is not settable.
	if ec := readEC(t, mustDispatch(sess, buildSetReceiveFolder(0, inboxEID, "IPM"), logonH, 0), ropSetReceiveFolder); ec != ecAccessDenied {
		t.Errorf("SetReceiveFolder(IPM) ec = %#x, want ecAccessDenied", ec)
	}
	// The empty default cannot be removed with a zero folder.
	if ec := readEC(t, mustDispatch(sess, buildSetReceiveFolder(0, 0, ""), logonH, 0), ropSetReceiveFolder); ec != ecError {
		t.Errorf("SetReceiveFolder(remove default) ec = %#x, want ecError", ec)
	}

	// GetReceiveFolderTable: the mappings (seeded 4 + the custom one), first row decodable.
	tblResp, _ := sess.Dispatch(toROPRequest(ropGetReceiveFolderTable, 0, nil), []uint32{logonH})
	p = ext.NewPull(tblResp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropGetReceiveFolderTable {
		t.Fatalf("GetReceiveFolderTable RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetReceiveFolderTable ec = %#x", ec)
	}
	if rc := mustU32(t, p, "RowCount"); rc < 5 {
		t.Errorf("RowCount = %d, want >= 5 (4 seeded + custom)", rc)
	}
	row := decodeRow(t, p, receiveFolderColumns)
	if _, ok := row.Get(mapi.PrFolderID); !ok {
		t.Error("table row missing PidTagFolderId")
	}

	// GetStoreState is not implemented (Exchange 2010+).
	if ec := readEC(t, mustDispatch(sess, toROPRequest(ropGetStoreState, 0, nil), logonH, 0), ropGetStoreState); ec != ecNotImplemented {
		t.Errorf("GetStoreState ec = %#x, want ecNotImplemented", ec)
	}
}
