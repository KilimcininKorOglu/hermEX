package admin

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// htmxGET issues a CSRF-bearing GET, the shape an htmx hx-get with hx-headers sends
// (the UI panel endpoints authorize every method, GET included).
func htmxGET(t *testing.T, ts *httptest.Server, path, session, csrf string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	req.Header.Set(csrfHeader, csrf)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// folderUserDir is a system-admin directory whose one user has a known maildir, so
// the folder handlers resolve the mailbox store path.
func folderUserDir() *fakeDir {
	return &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: directory.UserDetail{Username: "alice@hermex.test", Maildir: "/mb/alice"},
	}
}

// TestListUserFolders proves a system admin reads a user's folder tree.
func TestListUserFolders(t *testing.T) {
	d := folderUserDir()
	parent := int64(0x9)
	store := &fakeStore{folders: map[string][]objectstore.FolderInfo{
		"/mb/alice": {{ID: 0xC, DisplayName: "Inbox"}, {ID: 0x12, DisplayName: "Project", ParentID: &parent}},
	}}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/folders", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list folders status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"displayName":"Inbox"`) || !strings.Contains(string(body), `"displayName":"Project"`) {
		t.Errorf("folder body = %s, want both folders", body)
	}
}

// TestListFolderPermissions proves the permission members of a folder are returned
// with their named permission level.
func TestListFolderPermissions(t *testing.T) {
	d := folderUserDir()
	store := &fakeStore{folderPerms: map[string][]objectstore.PermissionEntry{
		"/mb/alice": {{MemberID: 5, Name: "bob@hermex.test", Rights: mapi.RightsEditor}},
	}}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/folders/12/permissions", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list permissions status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"name":"bob@hermex.test"`) || !strings.Contains(string(body), `"level":"Editor"`) {
		t.Errorf("permission body = %s, want bob as Editor", body)
	}
}

// TestSetFolderPermission proves a grant writes through with the requested rights.
func TestSetFolderPermission(t *testing.T) {
	d := folderUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	body := fmt.Sprintf(`{"username":"bob@hermex.test","rights":%d}`, mapi.RightsReviewer)
	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/folders/12/permissions", session, csrf, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set permission status %d, want 204", resp.StatusCode)
	}
	if store.setPermDir != "/mb/alice" || store.setPermFolder != 12 || store.setPermUser != "bob@hermex.test" || store.setPermRights != mapi.RightsReviewer {
		t.Errorf("captured = %q/%d/%q/%#x, want /mb/alice/12/bob/Reviewer",
			store.setPermDir, store.setPermFolder, store.setPermUser, store.setPermRights)
	}
}

// TestRemoveFolderPermission proves a delete drops the member addressed by id.
func TestRemoveFolderPermission(t *testing.T) {
	d := folderUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := authedDELETE(t, ts, "/admin/users/alice@hermex.test/folders/12/permissions?memberID=5", session, csrf, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove permission status %d, want 204", resp.StatusCode)
	}
	if store.rmPermDir != "/mb/alice" || store.rmPermFolder != 12 || store.rmPermMember != 5 {
		t.Errorf("captured = %q/%d/%d, want /mb/alice/12/5", store.rmPermDir, store.rmPermFolder, store.rmPermMember)
	}
}

// TestFolderPermissionsRequireSystem proves a domain admin cannot read or write a
// user's folder permissions through the system-scoped endpoints.
func TestFolderPermissionsRequireSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/folders", session)
	get.Body.Close()
	put := authedPUT(t, ts, "/admin/users/alice@hermex.test/folders/12/permissions", session, csrf, `{"username":"x@y.test","rights":1}`)
	put.Body.Close()
	if get.StatusCode != http.StatusForbidden || put.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin folders = GET %d / PUT %d, want both 403", get.StatusCode, put.StatusCode)
	}
}

// TestUIUserDetailShowsFolders proves the detail page renders the Folders section with
// the user's folders as picker options.
func TestUIUserDetailShowsFolders(t *testing.T) {
	d := folderUserDir()
	store := &fakeStore{folders: map[string][]objectstore.FolderInfo{
		"/mb/alice": {{ID: 0xC, DisplayName: "Inbox"}, {ID: 0x12, DisplayName: "Project"}},
	}}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"<h2>Folders</h2>", `id="folder-perms"`, ">Project<"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail page folders section missing %q", want)
		}
	}
}

// TestUIFolderPerms proves selecting a folder renders its permission panel: the member
// list and the grant form with the level dropdown.
func TestUIFolderPerms(t *testing.T) {
	d := folderUserDir()
	store := &fakeStore{folderPerms: map[string][]objectstore.PermissionEntry{
		"/mb/alice": {{MemberID: 5, Name: "bob@hermex.test", Rights: mapi.RightsEditor}},
	}}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := htmxGET(t, ts, "/admin/ui/users/alice@hermex.test/folder-perms?fid=12", session, csrf)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("folder-perms status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"bob@hermex.test", "Editor", "Grant", ">Owner<", ">Reviewer<"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("folder-perms panel missing %q:\n%s", want, body)
		}
	}
}

// TestUISetFolderPerm proves the grant form writes the chosen member and level through.
func TestUISetFolderPerm(t *testing.T) {
	d := folderUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/folder-perms/set", session, csrf, url.Values{
		"fid":      {"12"},
		"username": {"bob@hermex.test"},
		"rights":   {fmt.Sprintf("%d", mapi.RightsReviewer)},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui set permission status %d, want 200", resp.StatusCode)
	}
	if store.setPermDir != "/mb/alice" || store.setPermFolder != 12 || store.setPermUser != "bob@hermex.test" || store.setPermRights != mapi.RightsReviewer {
		t.Errorf("captured = %q/%d/%q/%#x, want /mb/alice/12/bob/Reviewer",
			store.setPermDir, store.setPermFolder, store.setPermUser, store.setPermRights)
	}
}

// TestUIRemoveFolderPerm proves the remove control drops the addressed member.
func TestUIRemoveFolderPerm(t *testing.T) {
	d := folderUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/folder-perms/remove", session, csrf, url.Values{
		"fid":      {"12"},
		"memberID": {"5"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui remove permission status %d, want 200", resp.StatusCode)
	}
	if store.rmPermDir != "/mb/alice" || store.rmPermFolder != 12 || store.rmPermMember != 5 {
		t.Errorf("captured = %q/%d/%d, want /mb/alice/12/5", store.rmPermDir, store.rmPermFolder, store.rmPermMember)
	}
}

// knownAlice is a directory that resolves only alice's primary address, so a grant to
// any other address is an unknown user.
func knownAlice() *fakeDir {
	d := folderUserDir()
	d.knownUsers = map[string]directory.UserDetail{
		"alice@hermex.test": {Username: "alice@hermex.test", Maildir: "/mb/alice"},
	}
	return d
}

// TestSetFolderPermissionRejectsUnknownMember proves a grant to an address that names
// no real user is refused — not silently stored as a row the access path (which
// compares the stored name verbatim against the login) would never match.
func TestSetFolderPermissionRejectsUnknownMember(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, knownAlice(), store)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/folders/12/permissions", session, csrf,
		`{"username":"ghost@hermex.test","rights":1}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown-member grant status %d, want 404", resp.StatusCode)
	}
	if store.setPermUser != "" {
		t.Errorf("unknown member was stored as %q, want no store call", store.setPermUser)
	}
}

// TestSetFolderPermissionCanonicalizes proves a mixed-case, padded address is stored
// in the lowercased trimmed form the access path compares against.
func TestSetFolderPermissionCanonicalizes(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store) // resolves any user
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/folders/12/permissions", session, csrf,
		`{"username":"  BOB@Hermex.Test  ","rights":1}`)
	resp.Body.Close()
	if store.setPermUser != "bob@hermex.test" {
		t.Errorf("stored member = %q, want lowercased bob@hermex.test", store.setPermUser)
	}
}

// TestUISetFolderPermRejectsUnknownMember proves the grant form refuses an unknown
// address and reports it in the panel rather than storing an inert grant.
func TestUISetFolderPermRejectsUnknownMember(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, knownAlice(), store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/folder-perms/set", session, csrf, url.Values{
		"fid":      {"12"},
		"username": {"ghost@hermex.test"},
		"rights":   {fmt.Sprintf("%d", mapi.RightsReviewer)},
	})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if store.setPermUser != "" {
		t.Errorf("unknown member was stored as %q, want no store call", store.setPermUser)
	}
	if !strings.Contains(string(body), "No such user") {
		t.Errorf("panel did not report the unknown user:\n%s", body)
	}
}
