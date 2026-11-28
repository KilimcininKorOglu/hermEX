package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// folderRowProps opens a folder on a logon and reads the given columns.
func folderRowProps(t *testing.T, sess *Session, logonH uint32, fid uint64, cols []mapi.PropTag) mapi.PropertyValues {
	t.Helper()
	folderH := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, fid))), ropOpenFolder, "OpenFolder", logonH, 0xFFFFFFFF)[1]
	out := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), folderH)
	return decodeRow(t, ropOK(t, out, ropGetPropertiesSpecific, "GetPropertiesSpecific(folder)"), cols)
}

// TestFolderEntryIDs proves a folder carries its own entry id and its parent's
// ([MS-OXCDATA] 2.2.4.1, [MS-OXCFOLD] 2.2.2.2.1.7): a private folder id under the
// MailboxGuid the logon reports, whose global counter is the folder's.
func TestFolderEntryIDs(t *testing.T) {
	dir := provisionedDir(t)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store
	guid, err := store.StoreGUID()
	if err != nil {
		t.Fatal(err)
	}

	row := folderRowProps(t, sess, logonH, mapi.PrivateFIDInbox, []mapi.PropTag{mapi.PrEntryID, mapi.PrParentEntryID})
	for tag, fid := range map[mapi.PropTag]uint64{mapi.PrEntryID: mapi.PrivateFIDInbox, mapi.PrParentEntryID: mapi.PrivateFIDIPMSubtree} {
		v := rowProp(t, row, tag, tag.String())
		raw, ok := v.([]byte)
		if !ok {
			t.Fatalf("%v = %T, want []byte", tag, v)
		}
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
// ([MS-OXCPRPT] 2.2.1.1): the owner may do everything, a reviewer only read, and
// a caller without Visible right is refused OpenFolder and leaves grants intact.
func TestFolderRightsAndAccess(t *testing.T) {
	dir := provisionedDir(t)
	const delegate = "delegate@hermex.test"
	const outsider = "outsider@hermex.test"
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDInbox), delegate, mapi.RightsReviewer|mapi.FrightsFreeBusySimple|mapi.FrightsFreeBusyDetailed)
	cols := []mapi.PropTag{mapi.PrRights, mapi.PrAccess}

	sess := NewSession(dir, nil, "")
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	owner := folderRowProps(t, sess, logonH, mapi.PrivateFIDInbox, cols)
	sess.Close()
	wantProp(t, owner, mapi.PrRights, int32(mapi.RightsAll), "owner rights")
	wantProp(t, owner, mapi.PrAccess, accessRead|accessModify|accessDelete|accessCreateAssoc|accessCreateHierarchy|accessCreateContents, "owner access")

	osess, outsiderLogon := delegateLogon(t, dir, outsider)
	defer osess.Close()
	denied := dispatchOn(osess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), outsiderLogon, 0xFFFFFFFF)
	wantEC(t, denied, ropOpenFolder, ecAccessDenied, "ungranted caller OpenFolder")

	dsess, dlogon := delegateLogon(t, dir, delegate)
	defer dsess.Close()
	row := folderRowProps(t, dsess, dlogon, mapi.PrivateFIDInbox, cols)
	wantProp(t, row, mapi.PrRights, int32(mapi.RightsReviewer), "reviewer rights")
	wantProp(t, row, mapi.PrAccess, accessRead, "reviewer access")
}
