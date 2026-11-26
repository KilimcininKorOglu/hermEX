package objectstore

import (
	"errors"
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

	child := mustCreateFolder(t, st, &a, "Projects")
	wantEq(t, "after adding a subfolder", hcn(a), a0+1)
	// A create refused for a sibling's name adds no subfolder, so it must not move
	// the counter a client diffs the hierarchy by.
	if _, err := st.CreateFolder(&a, "Projects"); !errors.Is(err, ErrFolderExists) {
		t.Fatalf("second Projects: err = %v, want ErrFolderExists", err)
	}
	wantEq(t, "after a refused create", hcn(a), a0+1)

	mustNoErr(t, "move the subfolder", st.RenameFolder(child, &b, "Projects"))
	wantEq(t, "after a subfolder left", hcn(a), a0+2)
	wantEq(t, "after a subfolder arrived", hcn(b), b0+1)
	mustNoErr(t, "rename in place", st.RenameFolder(child, &b, "Renamed"))
	wantEq(t, "a rename in place (not a subfolder change)", hcn(b), b0+1)
	mustNoErr(t, "delete the subfolder", st.DeleteFolder(child))
	wantEq(t, "after removing a subfolder", hcn(b), b0+2)
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
