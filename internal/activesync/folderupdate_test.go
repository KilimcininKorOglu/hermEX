package activesync

import (
	"strconv"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// folderUpdateReq builds a FolderUpdate request renaming/re-parenting a folder.
func folderUpdateReq(syncKey, serverID, parentID, name string) *wbxml.Node {
	return wbxml.Elem(wbxml.FHFolderUpdate,
		wbxml.Str(wbxml.FHSyncKey, syncKey),
		wbxml.Str(wbxml.FHServerID, serverID),
		wbxml.Str(wbxml.FHParentID, parentID),
		wbxml.Str(wbxml.FHDisplayName, name),
	)
}

// TestFolderUpdateRename proves renaming a folder reports success with an advanced
// key and the store reflects the new name.
func TestFolderUpdateRename(t *testing.T) {
	ts, dir := seededServer(t)
	key := primeHierarchy(t, ts)

	_, created := postCommand(t, ts, "FolderCreate", folderCreateReq(key, "0", "Old"))
	id := created.ChildText(wbxml.FHServerID)
	key2 := created.ChildText(wbxml.FHSyncKey)

	_, root := postCommand(t, ts, "FolderUpdate", folderUpdateReq(key2, id, "0", "New"))
	if s := root.ChildText(wbxml.FHStatus); s != "1" {
		t.Fatalf("FolderUpdate Status = %q, want 1 (success)", s)
	}
	if k := root.ChildText(wbxml.FHSyncKey); k == "" || k == key2 {
		t.Errorf("FolderUpdate sync key = %q, want a value advanced from %q", k, key2)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, exists, _ := st.FolderByName(nil, "New"); !exists {
		t.Error("renamed folder not found under its new name")
	}
	if _, exists, _ := st.FolderByName(nil, "Old"); exists {
		t.Error("old folder name still resolves after rename")
	}
}

// TestFolderUpdateReparent proves re-parenting a folder under another folder moves
// it in the store hierarchy.
func TestFolderUpdateReparent(t *testing.T) {
	ts, dir := seededServer(t)
	key := primeHierarchy(t, ts)

	_, p := postCommand(t, ts, "FolderCreate", folderCreateReq(key, "0", "Parent"))
	parentID := p.ChildText(wbxml.FHServerID)
	key2 := p.ChildText(wbxml.FHSyncKey)
	_, c := postCommand(t, ts, "FolderCreate", folderCreateReq(key2, "0", "Child"))
	childID := c.ChildText(wbxml.FHServerID)
	key3 := c.ChildText(wbxml.FHSyncKey)

	_, root := postCommand(t, ts, "FolderUpdate", folderUpdateReq(key3, childID, parentID, "Child"))
	if s := root.ChildText(wbxml.FHStatus); s != "1" {
		t.Fatalf("re-parent Status = %q, want 1", s)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pid, _ := strconv.ParseInt(parentID, 10, 64)
	// the child now resolves under the new parent, not at the root
	if _, exists, _ := st.FolderByName(&pid, "Child"); !exists {
		t.Error("re-parented folder not found under its new parent")
	}
	if _, exists, _ := st.FolderByName(nil, "Child"); exists {
		t.Error("re-parented folder still resolves at the mailbox root")
	}
}

// TestFolderUpdateDistinguished proves a built-in folder cannot be renamed
// (Status 3).
func TestFolderUpdateDistinguished(t *testing.T) {
	ts, _ := seededServer(t)
	key := primeHierarchy(t, ts)

	inbox := strconv.FormatInt(int64(mapi.PrivateFIDInbox), 10)
	_, root := postCommand(t, ts, "FolderUpdate", folderUpdateReq(key, inbox, "0", "MyInbox"))
	if s := root.ChildText(wbxml.FHStatus); s != "3" {
		t.Errorf("rename-Inbox Status = %q, want 3 (special folder)", s)
	}
}

// TestFolderUpdateDuplicate proves renaming a folder to a name already held by a
// sibling reports Status 2.
func TestFolderUpdateDuplicate(t *testing.T) {
	ts, _ := seededServer(t)
	key := primeHierarchy(t, ts)

	_, a := postCommand(t, ts, "FolderCreate", folderCreateReq(key, "0", "Alpha"))
	key2 := a.ChildText(wbxml.FHSyncKey)
	_, b := postCommand(t, ts, "FolderCreate", folderCreateReq(key2, "0", "Beta"))
	betaID := b.ChildText(wbxml.FHServerID)
	key3 := b.ChildText(wbxml.FHSyncKey)

	_, root := postCommand(t, ts, "FolderUpdate", folderUpdateReq(key3, betaID, "0", "Alpha"))
	if s := root.ChildText(wbxml.FHStatus); s != "2" {
		t.Errorf("collision Status = %q, want 2 (name exists)", s)
	}
}

// TestFolderUpdateBadSyncKey proves a mismatched sync key reports Status 9.
func TestFolderUpdateBadSyncKey(t *testing.T) {
	ts, _ := seededServer(t)
	key := primeHierarchy(t, ts)
	_, created := postCommand(t, ts, "FolderCreate", folderCreateReq(key, "0", "X"))
	id := created.ChildText(wbxml.FHServerID)

	_, root := postCommand(t, ts, "FolderUpdate", folderUpdateReq("999", id, "0", "Y"))
	if s := root.ChildText(wbxml.FHStatus); s != "9" {
		t.Errorf("bad-sync-key Status = %q, want 9", s)
	}
}
