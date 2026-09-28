package objectstore

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// folderCounter reads one of a folder's stored counters, 0 when unset.
func folderCounter(t *testing.T, st *Store, fid int64, tag mapi.PropTag) int32 {
	t.Helper()
	props, err := st.GetFolderProperties(fid, tag)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := props.Get(tag)
	n, _ := v.(int32)
	return n
}

// TestHierarchyChangeNumberCountsSubfolderChanges proves a folder's
// PidTagHierarchyChangeNumber rises each time a subfolder is added to it or
// removed from it, a move counting in both parents ([MS-OXCFOLD] 2.2.2.2.1.8).
func TestHierarchyChangeNumberCountsSubfolderChanges(t *testing.T) {
	st := openSeededStore(t)
	a, b := int64(mapi.PrivateFIDInbox), int64(mapi.PrivateFIDSentItems)
	hcn := func(fid int64) int32 { return folderCounter(t, st, fid, mapi.PrHierarchyChangeNum) }
	a0, b0 := hcn(a), hcn(b)

	child, err := st.CreateFolder(&a, "Projects")
	if err != nil {
		t.Fatal(err)
	}
	if got := hcn(a); got != a0+1 {
		t.Errorf("after adding a subfolder: %d, want %d", got, a0+1)
	}
	if err := st.RenameFolder(child, &b, "Projects"); err != nil {
		t.Fatal(err)
	}
	if got := hcn(a); got != a0+2 {
		t.Errorf("after a subfolder left: %d, want %d", got, a0+2)
	}
	if got := hcn(b); got != b0+1 {
		t.Errorf("after a subfolder arrived: %d, want %d", got, b0+1)
	}
	if err := st.RenameFolder(child, &b, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if got := hcn(b); got != b0+1 {
		t.Errorf("a rename in place counted as a subfolder change: %d, want %d", got, b0+1)
	}
	if err := st.DeleteFolder(child); err != nil {
		t.Fatal(err)
	}
	if got := hcn(b); got != b0+2 {
		t.Errorf("after removing a subfolder: %d, want %d", got, b0+2)
	}
}

// TestDeletedCountTotalCountsDeletedMessages proves a folder's
// PidTagDeletedCountTotal counts each message deleted from it once
// ([MS-OXCFOLD] 2.2.2.2.1.15): purging a message already in the dumpster does not
// count it again.
func TestDeletedCountTotalCountsDeletedMessages(t *testing.T) {
	st := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	newMessage := func() int64 {
		id, err := st.CreateMessage(inbox, &oxcmail.Message{Props: mapi.PropertyValues{{Tag: mapi.PrMessageClass, Value: "IPM.Note"}}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	deleted := func() int32 { return folderCounter(t, st, inbox, mapi.PrDeletedCountTotal) }
	start := deleted()

	first := newMessage()
	if err := st.SoftDeleteObject(first); err != nil {
		t.Fatal(err)
	}
	if got := deleted(); got != start+1 {
		t.Errorf("after a soft delete: %d, want %d", got, start+1)
	}
	if err := st.DeleteObject(first); err != nil {
		t.Fatal(err)
	}
	if got := deleted(); got != start+1 {
		t.Errorf("purging a dumpster message counted it again: %d, want %d", got, start+1)
	}
	if err := st.DeleteObject(newMessage()); err != nil {
		t.Fatal(err)
	}
	if got := deleted(); got != start+2 {
		t.Errorf("after a hard delete: %d, want %d", got, start+2)
	}
}
