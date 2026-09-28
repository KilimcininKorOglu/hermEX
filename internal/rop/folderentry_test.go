package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// folderRowProps opens a folder on a logon and reads the given columns.
func folderRowProps(t *testing.T, sess *Session, logonH uint32, fid uint64, cols []mapi.PropTag) mapi.PropertyValues {
	t.Helper()
	_, h := sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, fid))), []uint32{logonH, 0xFFFFFFFF})
	out, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{h[1]})
	return decodeRow(t, ropOK(t, out, ropGetPropertiesSpecific, "GetPropertiesSpecific(folder)"), cols)
}

// TestFolderEntryIDs proves a folder carries its own entry id and its parent's
// ([MS-OXCDATA] 2.2.4.1, [MS-OXCFOLD] 2.2.2.2.1.7): a private folder id under the
// MailboxGuid the logon reports, whose global counter is the folder's.
func TestFolderEntryIDs(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	store := sess.get(h[0]).store
	guid, err := store.StoreGUID()
	if err != nil {
		t.Fatal(err)
	}

	row := folderRowProps(t, sess, h[0], mapi.PrivateFIDInbox, []mapi.PropTag{mapi.PrEntryID, mapi.PrParentEntryID})
	for tag, fid := range map[mapi.PropTag]uint64{mapi.PrEntryID: mapi.PrivateFIDInbox, mapi.PrParentEntryID: mapi.PrivateFIDIPMSubtree} {
		v, _ := row.Get(tag)
		raw, _ := v.([]byte)
		if len(raw) != 46 {
			t.Fatalf("%v is %d bytes, want 46", tag, len(raw))
		}
		eid, err := ext.NewPull(raw, 0).FolderEntryID()
		if err != nil {
			t.Fatal(err)
		}
		if eid.ProviderUID != guid.Flat() || eid.FolderDBGUID != guid || eid.EIDType != eitLTPrivateFolder {
			t.Errorf("%v names another store or type: %+v", tag, eid)
		}
		if got := mapi.GCToValue(eid.FolderGC); got != fid {
			t.Errorf("%v names folder %#x, want %#x", tag, got, fid)
		}
	}
}

// TestFolderRightsAndAccess proves PidTagRights reports the caller's grant without
// the free/busy bits ([MS-OXCFOLD] 2.2.2.2.2.8) and PidTagAccess what it allows
// ([MS-OXCPRPT] 2.2.1.1): the owner may do everything, a reviewer only read.
func TestFolderRightsAndAccess(t *testing.T) {
	dir := t.TempDir()
	const delegate = "delegate@hermex.test"
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDInbox), delegate, mapi.RightsReviewer|mapi.FrightsFreeBusySimple)
	cols := []mapi.PropTag{mapi.PrRights, mapi.PrAccess}

	sess := NewSession(dir, nil, "")
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	owner := folderRowProps(t, sess, h[0], mapi.PrivateFIDInbox, cols)
	sess.Close()
	wantProp(t, owner, mapi.PrRights, int32(mapi.RightsAll), "owner rights")
	wantProp(t, owner, mapi.PrAccess, accessRead|accessModify|accessDelete|accessCreateAssoc|accessCreateHierarchy|accessCreateContents, "owner access")

	dsess, logonH := delegateLogon(t, dir, delegate)
	defer dsess.Close()
	row := folderRowProps(t, dsess, logonH, mapi.PrivateFIDInbox, cols)
	wantProp(t, row, mapi.PrRights, int32(mapi.RightsReviewer), "reviewer rights")
	wantProp(t, row, mapi.PrAccess, accessRead, "reviewer access")
}
