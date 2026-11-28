package admin

import (
	"fmt"
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
	req := newRequest(t, "GET", ts.URL+path, nil)
	req.Header.Set(csrfHeader, csrf)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	resp, err := http.DefaultClient.Do(req)
	mustNoErr(t, err, "htmxGET")
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

	unauth := authedGET(t, ts, "/admin/users/alice@hermex.test/folders", "invalid-session")
	wantStatus(t, unauth, http.StatusUnauthorized, "unauthenticated list folders")

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/folders", session)
	body := wantBody(t, resp, http.StatusOK, "list folders")
	if !strings.Contains(body, `"displayName":"Inbox"`) || !strings.Contains(body, `"displayName":"Project"`) {
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
	body := wantBody(t, resp, http.StatusOK, "list permissions")
	if !strings.Contains(body, `"name":"bob@hermex.test"`) || !strings.Contains(body, `"level":"Editor"`) {
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
	wantStatus(t, resp, http.StatusNoContent, "set permission")
	if store.setPermDir != "/mb/alice" || store.setPermFolder != 12 || store.setPermUser != "bob@hermex.test" || store.setPermRights != mapi.RightsReviewer {
		t.Errorf("captured = %q/%d/%q/%#x, want /mb/alice/12/bob/Reviewer",
			store.setPermDir, store.setPermFolder, store.setPermUser, store.setPermRights)
	}
}

// TestSetFolderPermissionMasksReservedBits sends every bit set. The store keeps
// rights verbatim, and every protocol masks a client's rights to RightsMaxROP
// ([MS-OXCPERM] 2.2.7) before writing, so the admin API must store only those
// bits, not the reserved ones a client could never set.
func TestSetFolderPermissionMasksReservedBits(t *testing.T) {
	d := folderUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/folders/12/permissions", session, csrf,
		`{"username":"bob@hermex.test","rights":4294967295}`), http.StatusNoContent, "set all bits")
	if store.setPermRights != mapi.RightsMaxROP {
		t.Errorf("stored rights = %#x, want only the settable bits %#x", store.setPermRights, mapi.RightsMaxROP)
	}
}

// TestRemoveFolderPermission proves a delete drops the member addressed by id.
func TestRemoveFolderPermission(t *testing.T) {
	d := folderUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := authedDELETE(t, ts, "/admin/users/alice@hermex.test/folders/12/permissions?memberID=5", session, csrf, "")
	wantStatus(t, resp, http.StatusNoContent, "remove permission")
	if store.rmPermDir != "/mb/alice" || store.rmPermFolder != 12 || store.rmPermMember != 5 {
		t.Errorf("captured = %q/%d/%d, want /mb/alice/12/5", store.rmPermDir, store.rmPermFolder, store.rmPermMember)
	}
}

// TestFolderPermissionsRequireSystem proves a domain admin cannot read or write the
// folder permissions of a user outside its scope through the user-scoped API, and
// cannot use the panel forms, which are system-only.
func TestFolderPermissionsRequireSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		domains: []directory.DomainInfo{{ID: 1, Name: "acme.test"}, {ID: 2, Name: "hermex.test"}},
		knownUsers: map[string]directory.UserDetail{
			"alice@hermex.test": {Username: "alice@hermex.test", DomainID: 2, Maildir: "/mb/alice"},
			"bob@acme.test":     {Username: "bob@acme.test", DomainID: 1, Maildir: "/mb/bob"},
		}}
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	// alice is in another domain; the grantee bob is in the caller's own, so only
	// the target user's scope can refuse the grant.
	get := authedGET(t, ts, "/admin/users/alice@hermex.test/folders", session)
	wantStatus(t, get, http.StatusForbidden, "domain-admin GET")
	put := authedPUT(t, ts, "/admin/users/alice@hermex.test/folders/12/permissions", session, csrf, `{"username":"bob@acme.test","rights":1}`)
	wantStatus(t, put, http.StatusForbidden, "domain-admin PUT")
	del := authedDELETE(t, ts, "/admin/users/alice@hermex.test/folders/12/permissions?memberID=5", session, csrf, "")
	wantStatus(t, del, http.StatusForbidden, "domain-admin DELETE")
	if get.StatusCode != http.StatusForbidden || put.StatusCode != http.StatusForbidden || del.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin folders = GET %d / PUT %d / DELETE %d, want all 403", get.StatusCode, put.StatusCode, del.StatusCode)
	}
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/folder-perms/set", session, csrf, url.Values{
		"fid": {"12"}, "username": {"x@y.test"}, "rights": {fmt.Sprintf("%d", mapi.RightsOwner)},
	}), http.StatusForbidden, "domain-admin UI grant")
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/folder-perms/remove", session, csrf, url.Values{
		"fid": {"12"}, "memberID": {"5"},
	}), http.StatusForbidden, "domain-admin UI remove")
	if store.setPermDir != "" || store.rmPermDir != "" {
		t.Errorf("a domain admin changed folder rights: set %q/%q, removed on %q", store.setPermDir, store.setPermUser, store.rmPermDir)
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

	body := wantBody(t, authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session), http.StatusOK, "user detail folders")
	for _, want := range []string{"<h2>Folders</h2>", `id="folder-perms"`, ">Project<"} {
		if !strings.Contains(body, want) {
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
	body := wantBody(t, resp, http.StatusOK, "folder-perms")
	for _, want := range []string{"bob@hermex.test", "Editor", "Grant", ">Owner<", ">Reviewer<"} {
		if !strings.Contains(body, want) {
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
	wantStatus(t, resp, http.StatusOK, "ui set permission")
	if store.setPermDir != "/mb/alice" || store.setPermFolder != 12 || store.setPermUser != "bob@hermex.test" || store.setPermRights != mapi.RightsReviewer {
		t.Errorf("captured = %q/%d/%q/%#x, want /mb/alice/12/bob/Reviewer",
			store.setPermDir, store.setPermFolder, store.setPermUser, store.setPermRights)
	}

	// A rights value that is not a number, or past 32 bits, read as 0 and silently
	// revoked every right the member held; it is now refused and nothing is written.
	for _, bad := range []string{"owner", "4294967296", ""} {
		store.setPermUser = ""
		body := wantBody(t, htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/folder-perms/set", session, csrf,
			url.Values{"fid": {"12"}, "username": {"bob@hermex.test"}, "rights": {bad}}), http.StatusOK, "rights "+bad)
		wantContains(t, body, "Choose a permission level", "the refusal for "+bad)
		if store.setPermUser != "" {
			t.Errorf("rights %q was written as %#x", bad, store.setPermRights)
		}
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
	wantStatus(t, resp, http.StatusOK, "ui remove permission")
	if store.rmPermDir != "/mb/alice" || store.rmPermFolder != 12 || store.rmPermMember != 5 {
		t.Errorf("captured = %q/%d/%d, want /mb/alice/12/5", store.rmPermDir, store.rmPermFolder, store.rmPermMember)
	}

	// A member id that is not a number read as 0, the default member, so a typo
	// removed the folder's default grant; it is now refused and nothing is removed.
	store.rmPermDir = ""
	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/folder-perms/remove", session, csrf,
		url.Values{"fid": {"12"}, "memberID": {"five"}}), http.StatusOK, "forged member id")
	wantContains(t, body, "member to remove is not valid", "a forged member id is refused")
	if store.rmPermDir != "" {
		t.Errorf("a forged member id removed member %d", store.rmPermMember)
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
// no real user is refused, not silently stored as a row the access path (which
// compares the stored name verbatim against the login) would never match.
func TestSetFolderPermissionRejectsUnknownMember(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, knownAlice(), store)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/folders/12/permissions", session, csrf,
		`{"username":"ghost@hermex.test","rights":1}`)
	wantStatus(t, resp, http.StatusNotFound, "unknown-member grant")
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
	wantStatus(t, resp, http.StatusNoContent, "canonicalize grant")
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
	body := wantBody(t, resp, http.StatusOK, "ui set permission unknown")
	if store.setPermUser != "" {
		t.Errorf("unknown member was stored as %q, want no store call", store.setPermUser)
	}
	if !strings.Contains(body, "No such user") {
		t.Errorf("panel did not report the unknown user:\n%s", body)
	}
}
