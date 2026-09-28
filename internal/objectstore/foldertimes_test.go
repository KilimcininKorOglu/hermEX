package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// folderTime reads one of a folder's computed times.
func folderTime(t *testing.T, st *Store, fid int64, tag mapi.PropTag) uint64 {
	t.Helper()
	props, err := st.FolderComputedProps(fid)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := props.Get(tag)
	if !ok {
		t.Fatalf("folder %d has no %#x", fid, uint32(tag))
	}
	return v.(uint64)
}

// tick lets the clock move on, so a later write stamps a later time.
func tick() { time.Sleep(2 * time.Millisecond) }

// TestFolderTimesFollowTheirContents proves a folder's commit and hierarchy times
// move with the writes they describe ([MS-OXCFOLD] 2.2.2.2.1.9, .13, .14): an edit
// to a message in the folder moves LocalCommitTime and LocalCommitTimeMax but not
// HierRev, and renaming a subfolder moves HierRev.
func TestFolderTimesFollowTheirContents(t *testing.T) {
	st := openSeededStore(t)
	parent := int64(mapi.PrivateFIDInbox)
	id, err := st.CreateMessage(parent, &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Note"}, {Tag: mapi.PrSubject, Value: "first"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := st.CreateFolder(&parent, "Projects")
	if err != nil {
		t.Fatal(err)
	}
	commit, commitMax, hier := folderTime(t, st, parent, mapi.PrLocalCommitTime),
		folderTime(t, st, parent, mapi.PrLocalCommitTimeMax), folderTime(t, st, parent, mapi.PrHierRev)

	tick()
	if err := st.SetMessageProperties(id, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "edited"}}); err != nil {
		t.Fatal(err)
	}
	if got := folderTime(t, st, parent, mapi.PrLocalCommitTimeMax); got <= commitMax {
		t.Errorf("LocalCommitTimeMax did not move with a message edit: %d <= %d", got, commitMax)
	}
	if got := folderTime(t, st, parent, mapi.PrLocalCommitTime); got <= commit {
		t.Errorf("LocalCommitTime did not move with a message edit: %d <= %d", got, commit)
	}
	if got := folderTime(t, st, parent, mapi.PrHierRev); got != hier {
		t.Errorf("HierRev moved with a message edit: %d != %d", got, hier)
	}

	tick()
	if err := st.SetFolderName(child, "Archive"); err != nil {
		t.Fatal(err)
	}
	if got := folderTime(t, st, parent, mapi.PrHierRev); got <= hier {
		t.Errorf("HierRev did not move with a subfolder rename: %d <= %d", got, hier)
	}
}
