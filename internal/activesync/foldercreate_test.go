package activesync

import (
	"net/http/httptest"
	"strconv"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// folderCreateReq builds a FolderCreate request for a folder of the given name
// under the given parent collection id.
func folderCreateReq(syncKey, parentID, name string) *wbxml.Node {
	return wbxml.Elem(wbxml.FHFolderCreate,
		wbxml.Str(wbxml.FHSyncKey, syncKey),
		wbxml.Str(wbxml.FHParentID, parentID),
		wbxml.Str(wbxml.FHDisplayName, name),
		wbxml.Str(wbxml.FHType, "12"),
	)
}

// primeHierarchy runs FolderSync 0 and returns the freshly primed hierarchy key,
// the same key the device must echo back on a FolderCreate.
func primeHierarchy(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	_, root := postCommand(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))
	key := root.ChildText(wbxml.FHSyncKey)
	if key == "" {
		t.Fatal("FolderSync 0 returned no hierarchy sync key")
	}
	return key
}

// folderSyncHasAdd reports whether a re-primed FolderSync advertises an Add for
// the given server id, the device's actual view of the hierarchy.
func folderSyncHasAdd(t *testing.T, ts *httptest.Server, serverID string) bool {
	t.Helper()
	_, root := postCommand(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))
	changes := root.Child(wbxml.FHChanges)
	if changes == nil {
		return false
	}
	for _, c := range changes.Children {
		if c.Tag == wbxml.FHAdd && c.ChildText(wbxml.FHServerID) == serverID {
			return true
		}
	}
	return false
}

// TestFolderCreate proves a FolderCreate creates a top-level folder, returns an
// advanced hierarchy key plus the new collection id, persists the folder, and
// surfaces it to a re-primed FolderSync (the device's hierarchy view).
func TestFolderCreate(t *testing.T) {
	ts, dir := seededServer(t)
	key := primeHierarchy(t, ts)

	_, root := postCommand(t, ts, "FolderCreate", folderCreateReq(key, "0", "Projects"))
	if s := root.ChildText(wbxml.FHStatus); s != "1" {
		t.Fatalf("FolderCreate Status = %q, want 1 (success)", s)
	}
	if k := root.ChildText(wbxml.FHSyncKey); k == "" || k == key {
		t.Errorf("FolderCreate sync key = %q, want a value advanced from %q", k, key)
	}
	newID := root.ChildText(wbxml.FHServerID)
	if newID == "" {
		t.Fatal("FolderCreate returned no server id")
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	fid, err := strconv.ParseInt(newID, 10, 64)
	mustNoErr(t, "parse the new folder id", err)
	ok, err := st.FolderExists(fid)
	mustNoErr(t, "look the folder up", err)
	if !ok {
		t.Errorf("created folder %s not found in store", newID)
	}
	_, exists, err := st.FolderByName(nil, "Projects")
	mustNoErr(t, "look the folder up by name", err)
	if !exists {
		t.Error("created folder not found by name at the mailbox root")
	}
	if !folderSyncHasAdd(t, ts, newID) {
		t.Error("created folder is not advertised by a re-primed FolderSync")
	}
}

// folderSyncType returns the Type a re-primed FolderSync advertises for a server
// id, "" when the folder is not advertised.
func folderSyncType(t *testing.T, ts *httptest.Server, serverID string) string {
	t.Helper()
	_, root := postCommand(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))
	for _, c := range root.Child(wbxml.FHChanges).Children {
		if c.Tag == wbxml.FHAdd && c.ChildText(wbxml.FHServerID) == serverID {
			return c.ChildText(wbxml.FHType)
		}
	}
	return ""
}

// TestFolderCreateMakesTheKindAsked proves the MS-ASCMD 2.2.3.186.2 Type
// (FolderCreate) of a user folder sets what the folder is: types 13-17 become a
// calendar, contacts, tasks, journal and notes folder (the container class every
// other protocol reads), and FolderSync reports each back with the type the
// device asked for (MS-ASCMD 2.2.3.186.3), so the device keeps the folder it made.
func TestFolderCreateMakesTheKindAsked(t *testing.T) {
	ts, dir := seededServer(t)
	cases := []struct{ typ, name, class string }{
		{"13", "Team", mapi.ContainerClassAppointment},
		{"14", "Clients", mapi.ContainerClassContact},
		{"15", "Chores", mapi.ContainerClassTask},
		{"16", "Log", mapi.ContainerClassJournal},
		{"17", "Jottings", mapi.ContainerClassStickyNote},
	}
	for _, c := range cases {
		req := folderCreateReq(primeHierarchy(t, ts), "0", c.name)
		req.Children[3].Text = c.typ
		_, root := postCommand(t, ts, "FolderCreate", req)
		if s := root.ChildText(wbxml.FHStatus); s != "1" {
			t.Fatalf("Type %s: Status = %q, want 1", c.typ, s)
		}
		id := root.ChildText(wbxml.FHServerID)
		fid, err := strconv.ParseInt(id, 10, 64)
		mustNoErr(t, "parse the folder id", err)
		st, err := objectstore.Open(dir)
		mustNoErr(t, "open the store", err)
		cls, err := folderClass(st, fid)
		mustNoErr(t, "close the store", st.Close())
		mustNoErr(t, "read the class", err)
		wantEq(t, "the class of Type "+c.typ, cls, c.class)
		wantEq(t, "the FolderSync Type of "+c.name, folderSyncType(t, ts, id), c.typ)
	}
}

// TestSyncServesAUserTaskFolder proves a task folder the device created is synced
// as a task collection: a task the device adds there is stored in that folder,
// not in the built-in Tasks folder, and is not refused as a mail folder would be.
func TestSyncServesAUserTaskFolder(t *testing.T) {
	ts, dir := seededServer(t)
	req := folderCreateReq(primeHierarchy(t, ts), "0", "Chores")
	req.Children[3].Text = "15"
	_, root := postCommand(t, ts, "FolderCreate", req)
	id := root.ChildText(wbxml.FHServerID)
	sync := func(key string, cmds ...*wbxml.Node) *wbxml.Node {
		coll := []*wbxml.Node{wbxml.Str(wbxml.ASSyncKey, key), wbxml.Str(wbxml.ASCollectionID, id)}
		if len(cmds) > 0 {
			coll = append(coll, wbxml.Elem(wbxml.ASCommands, cmds...))
		}
		return wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections, wbxml.Elem(wbxml.ASCollection, coll...)))
	}
	postCommand(t, ts, "Sync", sync("0"))
	add := wbxml.Elem(wbxml.ASAdd, wbxml.Str(wbxml.ASClientID, "cli-1"),
		wbxml.Elem(wbxml.ASData, wbxml.Str(wbxml.TKSubject, "Water plants")))
	_, root = postCommand(t, ts, "Sync", sync("1", add))
	addResp := respColl(t, root).Child(wbxml.ASResponses).Child(wbxml.ASAdd)
	if addResp == nil || addResp.ChildText(wbxml.ASStatus) != "1" {
		t.Fatalf("no successful Add response for the task: %+v", addResp)
	}
	mid, err := strconv.ParseInt(addResp.ChildText(wbxml.ASServerID), 10, 64)
	mustNoErr(t, "parse the task id", err)
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer mustClose(t, st)
	parent, err := st.MessageFolder(mid)
	mustNoErr(t, "find the task's folder", err)
	wantEq(t, "the folder the task was stored in", strconv.FormatInt(parent, 10), id)
}

// TestFolderCreateBadSyncKey proves a FolderCreate whose sync key does not match
// the device's hierarchy key reports Status 9 (the device must re-prime) and
// creates nothing.
func TestFolderCreateBadSyncKey(t *testing.T) {
	ts, dir := seededServer(t)
	primeHierarchy(t, ts)

	_, root := postCommand(t, ts, "FolderCreate", folderCreateReq("999", "0", "Nope"))
	if s := root.ChildText(wbxml.FHStatus); s != "9" {
		t.Errorf("bad-sync-key Status = %q, want 9", s)
	}

	wantRootFolder(t, dir, "Nope", false)
}

// TestFolderCreateDuplicate proves creating a second folder with a name already
// present at the parent reports Status 2.
func TestFolderCreateDuplicate(t *testing.T) {
	ts, _ := seededServer(t)
	key := primeHierarchy(t, ts)

	_, first := postCommand(t, ts, "FolderCreate", folderCreateReq(key, "0", "Dup"))
	// The refusal below only proves the duplicate rule if the first create worked.
	if s := first.ChildText(wbxml.FHStatus); s != "1" {
		t.Fatalf("first FolderCreate Status = %q, want 1", s)
	}
	key2 := first.ChildText(wbxml.FHSyncKey)
	if key2 == "" {
		t.Fatal("first FolderCreate returned no advanced sync key")
	}

	_, second := postCommand(t, ts, "FolderCreate", folderCreateReq(key2, "0", "Dup"))
	if s := second.ChildText(wbxml.FHStatus); s != "2" {
		t.Errorf("duplicate-name Status = %q, want 2", s)
	}
}

// TestFolderCreateRefusesAMalformedRequest proves the MS-ASCMD FolderCreate
// rules for the device's values: Status 10 ("malformed request ... or the client
// attempted to create a default folder") for a missing or empty DisplayName, a
// missing Type, a Type reserved for default folders (2-11 and 19, Type
// (FolderCreate)) and a Type outside the table; Status 5 for a ParentId that
// names no folder. Nothing is created in any of these cases.
func TestFolderCreateRefusesAMalformedRequest(t *testing.T) {
	ts, dir := seededServer(t)
	key := primeHierarchy(t, ts)
	req := func(parent, name, typ string, withName, withType bool) *wbxml.Node {
		kids := []*wbxml.Node{wbxml.Str(wbxml.FHSyncKey, key), wbxml.Str(wbxml.FHParentID, parent)}
		if withName {
			kids = append(kids, wbxml.Str(wbxml.FHDisplayName, name))
		}
		if withType {
			kids = append(kids, wbxml.Str(wbxml.FHType, typ))
		}
		return wbxml.Elem(wbxml.FHFolderCreate, kids...)
	}
	cases := []struct {
		name string
		body *wbxml.Node
		want string
	}{
		{"no DisplayName", req("0", "", "12", false, true), "10"},
		{"an empty DisplayName", req("0", "", "12", true, true), "10"},
		{"no Type", req("0", "Bad1", "", true, false), "10"},
		{"Type 2 (Inbox)", req("0", "Bad2", "2", true, true), "10"},
		{"Type 11", req("0", "Bad3", "11", true, true), "10"},
		{"Type 19", req("0", "Bad4", "19", true, true), "10"},
		{"Type 0", req("0", "Bad5", "0", true, true), "10"},
		{"Type 18", req("0", "Bad6", "18", true, true), "10"},
		{"Type not a number", req("0", "Bad7", "mail", true, true), "10"},
		{"a ParentId that is not a number", req("abc", "Bad8", "12", true, true), "5"},
		{"a negative ParentId", req("-5", "Bad9", "12", true, true), "5"},
	}
	for _, c := range cases {
		key = primeHierarchy(t, ts)
		c.body.Children[0].Text = key
		_, root := postCommand(t, ts, "FolderCreate", c.body)
		wantEq(t, "the Status for "+c.name, root.ChildText(wbxml.FHStatus), c.want)
	}
	for i := 1; i <= 9; i++ {
		wantRootFolder(t, dir, "Bad"+strconv.Itoa(i), false)
	}
	for _, typ := range []string{"1", "12"} {
		key = primeHierarchy(t, ts)
		_, root := postCommand(t, ts, "FolderCreate", req("0", "Good"+typ, typ, true, true))
		wantEq(t, "the Status for Type "+typ, root.ChildText(wbxml.FHStatus), "1")
	}
}

// TestFolderCreateBadParent proves a FolderCreate under a non-existent parent
// reports Status 5.
func TestFolderCreateBadParent(t *testing.T) {
	ts, dir := seededServer(t)
	key := primeHierarchy(t, ts)

	_, root := postCommand(t, ts, "FolderCreate", folderCreateReq(key, "888888", "Orphan"))
	if s := root.ChildText(wbxml.FHStatus); s != "5" {
		t.Errorf("missing-parent Status = %q, want 5", s)
	}
	// The refused folder is not created anywhere, the root included.
	wantRootFolder(t, dir, "Orphan", false)
}
