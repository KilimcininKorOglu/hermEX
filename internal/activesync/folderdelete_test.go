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
	defer mustClose(t, st)
	fid, err := strconv.ParseInt(newID, 10, 64)
	mustNoErr(t, "parse the folder id", err)
	// A failed lookup reports no folder, which would pass as a deletion.
	ok, err := st.FolderExists(fid)
	mustNoErr(t, "look up the deleted folder", err)
	if ok {
		t.Errorf("deleted folder %s still present in store", newID)
	}
	if folderSyncHasAdd(t, ts, newID) {
		t.Error("deleted folder is still advertised by a re-primed FolderSync")
	}
}

// TestFolderDeleteDistinguished proves every built-in folder a device sees is
// protected ([MS-ASCMD] 2.2.3.177.4, Status 3 "the specified folder is a special
// system folder") and survives the refused FolderDelete, not only the Inbox.
func TestFolderDeleteDistinguished(t *testing.T) {
	ts, dir := seededServer(t)
	builtins := []int64{
		mapi.PrivateFIDInbox, mapi.PrivateFIDDraft, mapi.PrivateFIDDeletedItems,
		mapi.PrivateFIDSentItems, mapi.PrivateFIDOutbox, mapi.PrivateFIDCalendar,
		mapi.PrivateFIDContacts, mapi.PrivateFIDTasks, mapi.PrivateFIDNotes,
		mapi.PrivateFIDJournal, mapi.PrivateFIDJunk,
	}
	for _, fid := range builtins {
		id := strconv.FormatInt(fid, 10)
		_, root := postCommand(t, ts, "FolderDelete", folderDeleteReq(primeHierarchy(t, ts), id))
		if s := root.ChildText(wbxml.FHStatus); s != "3" {
			t.Errorf("FolderDelete of built-in %s: Status = %q, want 3 (special folder)", id, s)
		}
		wantFolderPresent(t, dir, id)
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
// reports Status 9 ([MS-ASCMD] 2.2.3.177.4: the device must re-prime) and deletes
// nothing, since a stale device may name a folder from an outdated hierarchy.
func TestFolderDeleteBadSyncKey(t *testing.T) {
	ts, dir := seededServer(t)
	key := primeHierarchy(t, ts)
	_, created := postCommand(t, ts, "FolderCreate", folderCreateReq(key, "0", "Keep"))
	newID := created.ChildText(wbxml.FHServerID)

	_, root := postCommand(t, ts, "FolderDelete", folderDeleteReq("999", newID))
	if s := root.ChildText(wbxml.FHStatus); s != "9" {
		t.Errorf("bad-sync-key Status = %q, want 9", s)
	}
	wantFolderPresent(t, dir, newID)
}

// TestFolderDeleteRefusesAMalformedServerID proves a ServerId the server never
// issued is refused without deleting anything: an empty one is a malformed
// request (Status 10) and one that is not a folder id names no folder (Status 4),
// per [MS-ASCMD] 2.2.3.177.4.
func TestFolderDeleteRefusesAMalformedServerID(t *testing.T) {
	ts, dir := seededServer(t)
	key := primeHierarchy(t, ts)
	_, created := postCommand(t, ts, "FolderCreate", folderCreateReq(key, "0", "Keep"))
	keep := created.ChildText(wbxml.FHServerID)
	key = created.ChildText(wbxml.FHSyncKey)
	for id, want := range map[string]string{"": "10", "abc": "4", "../" + keep: "4", keep + " ": "4", "-" + keep: "4", "99999999999999999999": "4"} {
		_, root := postCommand(t, ts, "FolderDelete", folderDeleteReq(key, id))
		if s := root.ChildText(wbxml.FHStatus); s != want {
			t.Errorf("ServerId %q Status = %q, want %s", id, s, want)
		}
	}
	wantFolderPresent(t, dir, keep)
}

// wantFolderPresent fails when the folder with the given server id is gone.
func wantFolderPresent(t *testing.T, dir, serverID string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	fid, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := st.FolderExists(fid)
	mustNoErr(t, "look the folder up", err)
	if !ok {
		t.Errorf("folder %s was deleted by a refused FolderDelete", serverID)
	}
}
