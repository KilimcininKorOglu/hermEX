package objectstore

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// createInFolder stores one message with the given flags in a folder.
func createInFolder(t *testing.T, s *Store, fid int64, props mapi.PropertyValues) int64 {
	t.Helper()
	id, err := s.CreateMessage(fid, &oxcmail.Message{Props: props})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// computed returns one computed folder property.
func computed(t *testing.T, s *Store, fid int64, tag mapi.PropTag) any {
	t.Helper()
	props, err := s.FolderComputedProps(fid)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := props.Get(tag)
	return v
}

// TestFolderComputedProps proves a folder reports what it holds now
// ([MS-OXCFOLD] 2.2.2.2.1): its live non-FAI messages and the unread ones among
// them, their total size, and whether it has subfolders. An FAI message and a
// soft-deleted one are not counted.
func TestFolderComputedProps(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	createInFolder(t, s, inbox, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "unread"}})
	createInFolder(t, s, inbox, mapi.PropertyValues{{Tag: mapi.PrMessageFlags, Value: int32(mapi.MsgFlagRead)}})
	createInFolder(t, s, inbox, mapi.PropertyValues{{Tag: mapi.PrAssociated, Value: true}})
	gone := createInFolder(t, s, inbox, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "deleted"}})
	if err := s.DeleteObject(gone); err != nil {
		t.Fatal(err)
	}

	if v := computed(t, s, inbox, mapi.PrContentCount); v != int32(2) {
		t.Errorf("content count = %v, want 2 (FAI and deleted excluded)", v)
	}
	if v := computed(t, s, inbox, mapi.PrContentUnreadCount); v != int32(1) {
		t.Errorf("unread count = %v, want 1", v)
	}
	var want int64
	if err := s.objdb.QueryRow(`SELECT SUM(message_size) FROM messages WHERE parent_fid=? AND is_deleted=0 AND is_associated=0`, inbox).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if v := computed(t, s, inbox, mapi.PrMessageSizeExtended); v != want || want == 0 {
		t.Errorf("size = %v, want %d", v, want)
	}
	if v := computed(t, s, int64(mapi.PrivateFIDIPMSubtree), mapi.PrSubfolders); v != true {
		t.Error("the IPM subtree has subfolders")
	}
}

// TestFolderTypeAndFlags proves the root is FOLDER_ROOT, an IPM folder is generic
// and flagged IPM and NORMAL, and a folder outside the IPM subtree is not IPM.
func TestFolderTypeAndFlags(t *testing.T) {
	s := openSeededStore(t)
	for _, c := range []struct {
		fid        uint64
		typ, flags int32
	}{
		{mapi.PrivateFIDRoot, folderTypeRoot, 0},
		{mapi.PrivateFIDInbox, folderTypeGeneric, folderFlagIPM | folderFlagNormal},
		{mapi.PrivateFIDViews, folderTypeGeneric, folderFlagNormal},
	} {
		// #nosec G115 -- a builtin folder id
		fid := int64(c.fid)
		if v := computed(t, s, fid, mapi.PrFolderType); v != c.typ {
			t.Errorf("folder %#x type = %v, want %d", c.fid, v, c.typ)
		}
		if v := computed(t, s, fid, mapi.PrFolderFlags); v != c.flags {
			t.Errorf("folder %#x flags = %v, want %d", c.fid, v, c.flags)
		}
	}
}
