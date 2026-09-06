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

	// GetReceiveFolder("IPM.Note") resolves through the seeded "IPM" prefix to the
	// Inbox, and reports which class actually matched.
	wantReceiveFolder(t, sess, logonH, "IPM.Note", inboxEID, "IPM")

	// SetReceiveFolder: map IPM.Note.Custom to Sent Items, resolve a sub-class to it.
	wantSetReceiveFolder(t, sess, logonH, sentEID, "IPM.Note.Custom", ecSuccess)
	wantReceiveFolder(t, sess, logonH, "IPM.Note.Custom.Sub", sentEID, "IPM.Note.Custom")

	// IPM is not settable.
	wantSetReceiveFolder(t, sess, logonH, inboxEID, "IPM", ecAccessDenied)
	// The empty default cannot be removed with a zero folder.
	wantSetReceiveFolder(t, sess, logonH, 0, "", ecError)

	// GetReceiveFolderTable: the mappings (seeded 4 + the custom one), first row decodable.
	tblResp, _ := sess.Dispatch(toROPRequest(ropGetReceiveFolderTable, 0, nil), []uint32{logonH})
	p := ropOK(t, tblResp, ropGetReceiveFolderTable, "GetReceiveFolderTable")
	if rc := mustU32(t, p, "RowCount"); rc < 5 {
		t.Errorf("RowCount = %d, want >= 5 (4 seeded + custom)", rc)
	}
	row := decodeRow(t, p, receiveFolderColumns)
	if _, ok := row.Get(mapi.PrFolderID); !ok {
		t.Error("table row missing PidTagFolderId")
	}

	// GetStoreState is not implemented (Exchange 2010+).
	wantEC(t, mustDispatch(sess, toROPRequest(ropGetStoreState, 0, nil), logonH, 0), ropGetStoreState, ecNotImplemented, "GetStoreState")
}

// wantReceiveFolder resolves a message class and asserts both the folder it maps
// to and the class that actually matched.
func wantReceiveFolder(t *testing.T, sess *Session, logonH uint32, class string, wantEID uint64, wantExplicit string) {
	t.Helper()
	resp, _ := sess.Dispatch(buildGetReceiveFolder(0, class), []uint32{logonH})
	p := ropOK(t, resp, ropGetReceiveFolder, "GetReceiveFolder("+class+")")
	wantU64(t, p, "GetReceiveFolder("+class+") FolderId", wantEID)
	if explicit, _ := p.String8(); explicit != wantExplicit {
		t.Errorf("GetReceiveFolder(%s) explicit class = %q, want %q", class, explicit, wantExplicit)
	}
}

// wantSetReceiveFolder maps a class to a folder and asserts the return code.
func wantSetReceiveFolder(t *testing.T, sess *Session, logonH uint32, folderEID uint64, class string, want uint32) {
	t.Helper()
	resp := mustDispatch(sess, buildSetReceiveFolder(0, folderEID, class), logonH, 0)
	wantEC(t, resp, ropSetReceiveFolder, want, "SetReceiveFolder("+class+")")
}
