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
// rejects the un-settable IPM/REPORT.IPM classes, unknown folders, and the
// zero-folder default removal while preserving existing mappings; GetReceiveFolderTable
// returns the mappings; GetStoreState is not implemented.
func TestReceiveFolderROPs(t *testing.T) {
	dir := provisionedDir(t)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	sentEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDSentItems))

	// GetReceiveFolder("IPM.Note") resolves through the seeded "IPM" prefix to the
	// Inbox, and reports which class actually matched.
	wantReceiveFolder(t, sess, logonH, "IPM.Note", inboxEID, "IPM")

	// SetReceiveFolder: map IPM.Note.Custom to Sent Items, resolve a sub-class to it.
	wantSetReceiveFolder(t, sess, logonH, sentEID, "IPM.Note.Custom", ecSuccess)
	wantReceiveFolder(t, sess, logonH, "IPM.Note.Custom.Sub", sentEID, "IPM.Note.Custom")

	// Hostile targets: IPM and REPORT.IPM are not settable; zero-folder default cannot be removed;
	// non-existent folder and null handle are refused.
	wantSetReceiveFolder(t, sess, logonH, sentEID, "IPM", ecAccessDenied)
	wantSetReceiveFolder(t, sess, logonH, sentEID, "REPORT.IPM", ecAccessDenied)
	wantSetReceiveFolder(t, sess, logonH, 0, "", ecError)
	wantSetReceiveFolder(t, sess, logonH, uint64(mapi.MakeEIDEx(1, 0x7FFFFF)), "IPM.Note.Bogus", ecNotFound)
	wantSetReceiveFolder(t, sess, 0xFFFFFFFF, sentEID, "IPM.Note.BadHandle", ecError)

	// Refusals leave the existing mappings unchanged.
	wantReceiveFolder(t, sess, logonH, "IPM.Note", inboxEID, "IPM")
	wantReceiveFolder(t, sess, logonH, "", inboxEID, "")
	wantReceiveFolder(t, sess, logonH, "IPM.Note.Custom.Sub", sentEID, "IPM.Note.Custom")

	// GetReceiveFolderTable: the mappings (seeded 4 + the custom one), first row decodable.
	tblResp := dispatchOn(sess, toROPRequest(ropGetReceiveFolderTable, 0, nil), logonH)
	p := ropOK(t, tblResp, ropGetReceiveFolderTable, "GetReceiveFolderTable")
	if rc := mustU32(t, p, "RowCount"); rc < 5 {
		t.Errorf("RowCount = %d, want >= 5 (4 seeded + custom)", rc)
	}
	row := decodeRow(t, p, receiveFolderColumns)
	if _, ok := row.Get(mapi.PrFolderID); !ok {
		t.Error("table row missing PidTagFolderId")
	}

	// GetStoreState is not implemented (Exchange 2010+).
	wantEC(t, dispatchOn(sess, toROPRequest(ropGetStoreState, 0, nil), logonH), ropGetStoreState, ecNotImplemented, "GetStoreState")
}

// wantReceiveFolder resolves a message class and asserts both the folder it maps
// to and the class that actually matched.
func wantReceiveFolder(t *testing.T, sess *Session, logonH uint32, class string, wantEID uint64, wantExplicit string) {
	t.Helper()
	resp := dispatchOn(sess, buildGetReceiveFolder(0, class), logonH)
	p := ropOK(t, resp, ropGetReceiveFolder, "GetReceiveFolder("+class+")")
	wantU64(t, p, "GetReceiveFolder("+class+") FolderId", wantEID)
	explicit, err := p.String8()
	if err != nil {
		t.Fatalf("read ExplicitMessageClass: %v", err)
	}
	if explicit != wantExplicit {
		t.Errorf("GetReceiveFolder(%s) explicit class = %q, want %q", class, explicit, wantExplicit)
	}
}

// wantSetReceiveFolder maps a class to a folder and asserts the return code.
func wantSetReceiveFolder(t *testing.T, sess *Session, logonH uint32, folderEID uint64, class string, want uint32) {
	t.Helper()
	resp := dispatchOn(sess, buildSetReceiveFolder(0, folderEID, class), logonH)
	wantEC(t, resp, ropSetReceiveFolder, want, "SetReceiveFolder("+class+")")
}
