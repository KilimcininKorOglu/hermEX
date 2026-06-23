package webmail

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// userFolderByName looks up a top-level folder by display name.
func userFolderByName(t *testing.T, path, name string) (int64, bool) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, ok, err := st.FolderByName(nil, name)
	if err != nil {
		t.Fatal(err)
	}
	return id, ok
}

// parentOf returns a folder's parent id as ListFolders reports it (nil = top).
func parentOf(t *testing.T, path string, id int64) (*int64, bool) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	folders, err := st.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	return folderParent(folders, id)
}

func folderPost(t *testing.T, c *http.Client, base string, vals url.Values) (int, string) {
	t.Helper()
	return postForm(t, c, base+"/folder", vals)
}

// TestFolderCreate checks a new top-level folder is stored as a user folder and
// shown in the sidebar.
func TestFolderCreate(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	code, body := folderPost(t, c, ts.URL, url.Values{"op": {"create"}, "name": {"Projects"}})
	if code != 200 { // 303 → /mail, followed by the client
		t.Fatalf("create = %d", code)
	}
	if !strings.Contains(body, "Projects") {
		t.Errorf("sidebar does not show the new folder:\n%s", body)
	}
	if id, ok := userFolderByName(t, path, "Projects"); !ok || id < int64(mapi.PrivateFIDUnassignedStart) {
		t.Errorf("Projects not stored as a user folder: id=%d ok=%v", id, ok)
	}
}

// TestFolderCreateDedupe checks a duplicate top-level name is refused.
func TestFolderCreateDedupe(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	folderPost(t, c, ts.URL, url.Values{"op": {"create"}, "name": {"Projects"}})
	if code, _ := folderPost(t, c, ts.URL, url.Values{"op": {"create"}, "name": {"Projects"}}); code != http.StatusBadRequest {
		t.Errorf("duplicate create = %d, want 400", code)
	}
	// Exactly one "Projects" exists.
	st, _ := objectstore.Open(path)
	defer st.Close()
	folders, _ := st.ListFolders()
	n := 0
	for _, f := range folders {
		if f.DisplayName == "Projects" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("found %d folders named Projects, want 1", n)
	}
}

// TestFolderCreateInvalid checks empty and separator-bearing names are refused.
func TestFolderCreateInvalid(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	for _, name := range []string{"", "   ", "a/b"} {
		if code, _ := folderPost(t, c, ts.URL, url.Values{"op": {"create"}, "name": {name}}); code != http.StatusBadRequest {
			t.Errorf("create %q = %d, want 400", name, code)
		}
	}
	if _, ok := userFolderByName(t, path, "a/b"); ok {
		t.Errorf("an invalid name must not create a folder")
	}
}

// TestFolderCreateEscapesName checks a script-y folder name is HTML-escaped in
// the rendered sidebar (no reflected markup).
func TestFolderCreateEscapesName(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// A name with HTML-special chars but no "/" (a "/" would be rejected as a path
	// separator, so it could never reach the sidebar to test escaping).
	_, body := folderPost(t, c, ts.URL, url.Values{"op": {"create"}, "name": {"<pwn>"}})
	if strings.Contains(body, "<pwn>") {
		t.Errorf("folder name reflected raw (XSS):\n%s", body)
	}
	if !strings.Contains(body, "&lt;pwn&gt;") {
		t.Errorf("escaped folder name expected in the sidebar")
	}
}

// TestFolderRename checks a user folder's display name changes.
func TestFolderRename(t *testing.T) {
	path := emptyMailbox(t)
	id := makeFolder(t, path, "Projects")
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code, _ := folderPost(t, c, ts.URL, url.Values{"op": {"rename"}, "id": {fid64(id)}, "name": {"Work"}}); code != 200 {
		t.Fatalf("rename = %d", code)
	}
	if _, ok := userFolderByName(t, path, "Work"); !ok {
		t.Errorf("renamed folder Work not found")
	}
	if _, ok := userFolderByName(t, path, "Projects"); ok {
		t.Errorf("old name Projects still present after rename")
	}
}

// TestFolderRenameKeepsNesting guards the reparent bug: renaming a nested folder
// must keep it under its parent (passing nil newParent would move it to top).
func TestFolderRenameKeepsNesting(t *testing.T) {
	path := emptyMailbox(t)
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateFolder(nil, "Parent")
	if err != nil {
		t.Fatal(err)
	}
	cid, err := st.CreateFolder(&pid, "Child")
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code, _ := folderPost(t, c, ts.URL, url.Values{"op": {"rename"}, "id": {fid64(cid)}, "name": {"Renamed"}}); code != 200 {
		t.Fatalf("rename child = %d", code)
	}
	parent, found := parentOf(t, path, cid)
	if !found {
		t.Fatalf("renamed child folder not found")
	}
	if parent == nil || *parent != pid {
		t.Errorf("rename reparented the child: parent=%v, want %d", parent, pid)
	}
}

// TestFolderRenameBuiltinRejected checks a built-in folder cannot be renamed.
func TestFolderRenameBuiltinRejected(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code, _ := folderPost(t, c, ts.URL, url.Values{
		"op": {"rename"}, "id": {fid64(int64(mapi.PrivateFIDInbox))}, "name": {"Hacked"},
	}); code != http.StatusForbidden {
		t.Errorf("rename built-in Inbox = %d, want 403", code)
	}
	if _, ok := userFolderByName(t, path, "Hacked"); ok {
		t.Errorf("a rejected rename must not create the new name")
	}
}

// TestFolderDelete checks a user folder (with a message) is removed by the cascade.
func TestFolderDelete(t *testing.T) {
	path := emptyMailbox(t)
	id := makeFolder(t, path, "Trashme")
	seedMsg(t, path, id, "inside", "", "body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code, _ := folderPost(t, c, ts.URL, url.Values{"op": {"delete"}, "id": {fid64(id)}}); code != 200 {
		t.Fatalf("delete = %d", code)
	}
	if _, ok := userFolderByName(t, path, "Trashme"); ok {
		t.Errorf("deleted folder still present")
	}
}

// TestFolderDeleteBuiltinRejected checks a built-in folder cannot be deleted.
func TestFolderDeleteBuiltinRejected(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code, _ := folderPost(t, c, ts.URL, url.Values{
		"op": {"delete"}, "id": {fid64(int64(mapi.PrivateFIDDeletedItems))},
	}); code != http.StatusForbidden {
		t.Errorf("delete built-in Deleted Items = %d, want 403", code)
	}
	// Still listed.
	st, _ := objectstore.Open(path)
	defer st.Close()
	folders, _ := st.ListFolders()
	if _, found := folderParent(folders, int64(mapi.PrivateFIDDeletedItems)); !found {
		t.Errorf("Deleted Items missing after a rejected delete")
	}
}

// TestEmptyFolderMovesToTrash checks that emptying a non-trash folder moves every
// message to Deleted Items (recoverable), not permanent removal.
func TestEmptyFolderMovesToTrash(t *testing.T) {
	path := emptyMailbox(t)
	inbox := int64(mapi.PrivateFIDInbox)
	trash := int64(mapi.PrivateFIDDeletedItems)
	seedMsg(t, path, inbox, "a", "", "body", 100, 0)
	seedMsg(t, path, inbox, "b", "", "body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	folderPost(t, c, ts.URL, url.Values{"op": {"empty"}, "folder": {"INBOX"}})

	if n := len(folderMsgs(t, path, inbox)); n != 0 {
		t.Errorf("Inbox has %d after empty, want 0", n)
	}
	if n := len(folderMsgs(t, path, trash)); n != 2 {
		t.Errorf("Deleted Items has %d after emptying Inbox, want 2", n)
	}
}

// TestEmptyTrashPermanent checks that emptying Deleted Items permanently removes
// its messages — they do not loop back into Trash.
func TestEmptyTrashPermanent(t *testing.T) {
	path := emptyMailbox(t)
	trash := int64(mapi.PrivateFIDDeletedItems)
	seedMsg(t, path, trash, "x", "", "body", 100, 0)
	seedMsg(t, path, trash, "y", "", "body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	folderPost(t, c, ts.URL, url.Values{"op": {"empty"}, "folder": {"Deleted Items"}})

	if n := len(folderMsgs(t, path, trash)); n != 0 {
		t.Errorf("Deleted Items has %d after empty, want 0 (permanent)", n)
	}
}

// favoriteFolders loads a mailbox's persisted favorite folder list.
func favoriteFolders(t *testing.T, path string) []string {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg, err := loadSettings(st)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.FavoriteFolders
}

// TestFolderFavoriteToggle checks favoriting a folder pins it (persisted in
// settings and shown in a sidebar Favorites section) and toggling again unpins it.
func TestFolderFavoriteToggle(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	folderPost(t, c, ts.URL, url.Values{"op": {"favorite"}, "folder": {"INBOX"}})
	if favs := favoriteFolders(t, path); len(favs) != 1 || favs[0] != "INBOX" {
		t.Fatalf("after favorite, favorites = %v, want [INBOX]", favs)
	}
	if _, body := get(t, c, ts.URL+"/mail?folder=INBOX"); !strings.Contains(body, `sidebar-heading">Favorites`) {
		t.Errorf("sidebar did not render a Favorites section after favoriting:\n%s", body)
	}

	folderPost(t, c, ts.URL, url.Values{"op": {"favorite"}, "folder": {"INBOX"}})
	if favs := favoriteFolders(t, path); len(favs) != 0 {
		t.Errorf("after unfavorite, favorites = %v, want none", favs)
	}
}

// TestSidebarFolderSize checks the sidebar shows a folder's message count and
// total size as a tooltip on the folder link.
func TestSidebarFolderSize(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "hello", "", "some body content", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)
	if _, body := get(t, c, ts.URL+"/mail?folder=INBOX"); !strings.Contains(body, `title="1 message(s),`) {
		t.Errorf("sidebar folder link missing the count/size tooltip:\n%s", body)
	}
}

// TestFolderUnauthenticated checks /folder requires a session.
func TestFolderUnauthenticated(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	if code, _ := postForm(t, &http.Client{}, ts.URL+"/folder", url.Values{"op": {"create"}, "name": {"X"}}); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated /folder = %d, want 401", code)
	}
}
