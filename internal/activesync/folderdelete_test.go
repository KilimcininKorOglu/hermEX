package activesync

import (
	"strconv"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// folderDeleteReq builds a FolderDelete request for the given collection id.
func folderDeleteReq(syncKey, serverID string) *wbxml.Node {
	return wbxml.Elem(wbxml.FHFolderDelete,
		wbxml.Str(wbxml.FHSyncKey, syncKey),
		wbxml.Str(wbxml.FHServerID, serverID),
	)
}

// TestFolderDelete proves deleting a user-created folder reports success with an
// advanced hierarchy key, removes the folder from the store, and drops it from a
// re-primed FolderSync (the device's hierarchy view).
func TestFolderDelete(t *testing.T) {
	ts, dir := seededServer(t)
	key := primeHierarchy(t, ts)

	_, created := postCommand(t, ts, "FolderCreate", folderCreateReq(key, "0", "Temp"))
	newID := created.ChildText(wbxml.FHServerID)
	key2 := created.ChildText(wbxml.FHSyncKey)
	if newID == "" || key2 == "" {
		t.Fatal("FolderCreate did not return a server id and advanced key")
	}

	_, root := postCommand(t, ts, "FolderDelete", folderDeleteReq(key2, newID))
	if s := root.ChildText(wbxml.FHStatus); s != "1" {
		t.Fatalf("FolderDelete Status = %q, want 1 (success)", s)
	}
	if k := root.ChildText(wbxml.FHSyncKey); k == "" || k == key2 {
		t.Errorf("FolderDelete sync key = %q, want a value advanced from %q", k, key2)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fid, _ := strconv.ParseInt(newID, 10, 64)
	if ok, _ := st.FolderExists(fid); ok {
		t.Errorf("deleted folder %s still present in store", newID)
	}
	if folderSyncHasAdd(t, ts, newID) {
		t.Error("deleted folder is still advertised by a re-primed FolderSync")
	}
}

// TestFolderDeleteDistinguished proves a built-in folder (the Inbox) is protected
// and reports Status 3 without being deleted.
func TestFolderDeleteDistinguished(t *testing.T) {
	ts, dir := seededServer(t)
	key := primeHierarchy(t, ts)

	_, root := postCommand(t, ts, "FolderDelete", folderDeleteReq(key, inboxID()))
	if s := root.ChildText(wbxml.FHStatus); s != "3" {
		t.Errorf("delete-Inbox Status = %q, want 3 (special folder)", s)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if ok, _ := st.FolderExists(int64(mapi.PrivateFIDInbox)); !ok {
		t.Error("Inbox must survive a rejected FolderDelete")
	}
}

// TestFolderDeleteNotFound proves deleting a non-existent (but user-range) folder
// reports Status 4.
func TestFolderDeleteNotFound(t *testing.T) {
	ts, _ := seededServer(t)
	key := primeHierarchy(t, ts)

	_, root := postCommand(t, ts, "FolderDelete", folderDeleteReq(key, "999999"))
	if s := root.ChildText(wbxml.FHStatus); s != "4" {
		t.Errorf("delete-missing Status = %q, want 4 (not found)", s)
	}
}

// TestFolderDeleteBadSyncKey proves a FolderDelete whose sync key does not match
// reports Status 9.
func TestFolderDeleteBadSyncKey(t *testing.T) {
	ts, _ := seededServer(t)
	key := primeHierarchy(t, ts)
	_, created := postCommand(t, ts, "FolderCreate", folderCreateReq(key, "0", "Keep"))
	newID := created.ChildText(wbxml.FHServerID)

	_, root := postCommand(t, ts, "FolderDelete", folderDeleteReq("999", newID))
	if s := root.ChildText(wbxml.FHStatus); s != "9" {
		t.Errorf("bad-sync-key Status = %q, want 9", s)
	}
}
