package webmail2api

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestAclSanitizeRightsDropsForbiddenBits proves the ingest mask strips any bit
// outside RightsMaxROP (reserved / reference-private extensions) while preserving
// every standard named-level preset the SPA sends.
func TestAclSanitizeRightsDropsForbiddenBits(t *testing.T) {
	if got := aclSanitizeRights(0xFFFFFFFF); got != mapi.RightsMaxROP {
		t.Errorf("sanitize(all bits) = %#x, want RightsMaxROP %#x", got, mapi.RightsMaxROP)
	}
	for _, preset := range []uint32{
		mapi.RightsReviewer, mapi.RightsContributor, mapi.RightsNoneditingAuthor,
		mapi.RightsAuthor, mapi.RightsPublishingAuthor, mapi.RightsEditor,
		mapi.RightsPublishingEditor, mapi.RightsOwner, mapi.RightsNone,
	} {
		if got := aclSanitizeRights(preset); got != preset {
			t.Errorf("sanitize(%#x) = %#x, want the preset unchanged", preset, got)
		}
	}
}

func TestFolderDescendants(t *testing.T) {
	pid := func(v int64) *int64 { return &v }
	// tree: 1 -> {2 -> {4}, 3}, 5 (unrelated)
	folders := []objectstore.FolderInfo{
		{ID: 1, ParentID: nil},
		{ID: 2, ParentID: pid(1)},
		{ID: 3, ParentID: pid(1)},
		{ID: 4, ParentID: pid(2)},
		{ID: 5, ParentID: nil},
	}
	got := folderDescendants(folders, 1)
	want := map[int64]bool{2: true, 3: true, 4: true}
	if len(got) != len(want) {
		t.Fatalf("descendants(1) = %v, want ids %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("descendants(1) contains unexpected id %d", id)
		}
	}
	if leaf := folderDescendants(folders, 4); len(leaf) != 0 {
		t.Errorf("descendants(leaf 4) = %v, want none", leaf)
	}
}
