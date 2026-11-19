package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
)

// pubFoldersDir is a system-admin directory with one domain and two known users
// (an in-domain grantee and an out-of-domain one for the isolation check). The
// real per-domain public store lives under the test's temp HomedirFor.
func pubFoldersDir() *fakeDir {
	return &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domains: []directory.DomainInfo{{ID: 1, Name: "local.test"}},
		knownUsers: map[string]directory.UserDetail{
			"poster@local.test":   {Username: "poster@local.test"},
			"intruder@other.test": {Username: "intruder@other.test"},
		},
	}
}

// TestPublicGrantTakesTheMemberDomainAfterTheLastAt grants to an account whose
// quoted local part holds an '@' (RFC 5321 4.1.2). directory.ValidateAddress
// assigns it to the domain after the last '@', so it is a member of local.test
// and the grant must be accepted, not refused as another domain's user.
func TestPublicGrantTakesTheMemberDomainAfterTheLastAt(t *testing.T) {
	d := pubFoldersDir()
	d.knownUsers["x@other.test@local.test"] = directory.UserDetail{Username: "x@other.test@local.test"}
	s := &Server{dir: d}
	change, errMsg := s.publicGrantChange("local.test", "x@other.test@local.test", 1)
	if errMsg != "" || change.Username != "x@other.test@local.test" {
		t.Errorf("grant = %+v, %q; want it accepted for the local.test member", change, errMsg)
	}
}

func rightsValue(r uint32) string { return strconv.FormatUint(uint64(r), 10) }

// pubFolderID reads the JSON list and returns the id of the folder named name.
func pubFolderID(t *testing.T, ts *httptest.Server, session, name string) int64 {
	t.Helper()
	resp := authedGET(t, ts, "/admin/public-folders?domain=local.test", session)
	defer resp.Body.Close()
	var folders []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&folders); err != nil {
		t.Fatalf("decode public-folders JSON: %v", err)
	}
	for _, f := range folders {
		if f.Name == name {
			return f.ID
		}
	}
	t.Fatalf("folder %q not in JSON list %+v", name, folders)
	return 0
}

// TestPublicFoldersManage walks the full admin public-folder flow against a real
// per-domain store: create a folder (which provisions the store), grant "anyone"
// and an in-domain user, reject a cross-domain grantee, refuse a structural-folder
// delete, then delete the folder.
func TestPublicFoldersManage(t *testing.T) {
	d := pubFoldersDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	// The page renders with the domain picker.
	page := wantBody(t, authedGET(t, ts, "/admin/ui/public-folders", session), http.StatusOK, "public folders page")
	wantContains(t, page, "local.test", "the page offers the domain")

	// Creating a folder provisions the store and shows the folder. Assert the folder
	// HEADING, not the substring "Announcements" (which also appears in the create
	// form's placeholder).
	created := wantBody(t, htmxPOST(t, ts, "/admin/ui/public-folders/folder", session, csrf,
		url.Values{"domain": {"local.test"}, "name": {"Announcements"}}), http.StatusOK, "create folder")
	wantContains(t, created, "<h3>Announcements", "the created folder is listed")
	fid := pubFolderID(t, ts, session, "Announcements")

	grant := func(grantee string, rights uint32) string {
		t.Helper()
		return wantBody(t, htmxPOST(t, ts, "/admin/ui/public-folders/grant", session, csrf,
			url.Values{
				"domain": {"local.test"}, "fid": {strconv.FormatInt(fid, 10)},
				"grantee": {grantee}, "rights": {rightsValue(rights)},
			}), http.StatusOK, "grant to "+grantee)
	}

	// Grant "anyone" Reviewer; it appears relabelled as a grant ROW (not the
	// "anyone" placeholder text nor the "Reviewer" level-dropdown option).
	wantContains(t, grant("anyone", mapi.RightsReviewer), "<td>anyone</td><td>Reviewer</td>",
		"the anyone/Reviewer grant row shows")
	// Grant the in-domain poster Author rights (asserted as a grant row).
	wantContains(t, grant("poster@local.test", mapi.RightsAuthor), "<td>poster@local.test</td><td>Author</td>",
		"the poster/Author grant row shows")

	// A cross-domain grantee is rejected (the grant would be inert under tenant routing).
	crossDomain := grant("intruder@other.test", mapi.RightsReviewer)
	wantContains(t, crossDomain, "user in this domain", "a cross-domain grantee is refused")
	wantNotContains(t, crossDomain, "<td>intruder@other.test</td>", "the refused grantee is not stored")

	// A rights value that is not a number read as 0 and silently stripped the
	// poster's Author grant; it is now refused and the grant row keeps its level.
	forged := wantBody(t, htmxPOST(t, ts, "/admin/ui/public-folders/grant", session, csrf,
		url.Values{"domain": {"local.test"}, "fid": {strconv.FormatInt(fid, 10)},
			"grantee": {"poster@local.test"}, "rights": {"owner"}}), http.StatusOK, "forged rights")
	wantContains(t, forged, "Choose a permission level", "a forged rights value is refused")
	wantContains(t, forged, "<td>poster@local.test</td><td>Author</td>", "the refused change keeps the grant")

	// A member id that is not a number read as 0, the default ("anyone") member, so a
	// typo removed the org-wide grant; it is now refused and the row stays.
	badRemove := wantBody(t, htmxPOST(t, ts, "/admin/ui/public-folders/grant/remove", session, csrf,
		url.Values{"domain": {"local.test"}, "fid": {strconv.FormatInt(fid, 10)}, "memberID": {"x"}}),
		http.StatusOK, "forged member id")
	wantContains(t, badRemove, "member to remove is not valid", "a forged member id is refused")
	wantContains(t, badRemove, "<td>anyone</td><td>Reviewer</td>", "the default grant survives")

	deleteFolder := func(id int64) string {
		t.Helper()
		return wantBody(t, htmxPOST(t, ts, "/admin/ui/public-folders/folder/delete", session, csrf,
			url.Values{"domain": {"local.test"}, "fid": {strconv.FormatInt(id, 10)}}), http.StatusOK, "delete folder")
	}

	// A structural-folder delete (IPM_SUBTREE = 0x02) is refused, leaving the tree intact.
	structural := deleteFolder(int64(mapi.PublicFIDIPMSubtree))
	wantContains(t, structural, "structural folder", "a structural delete is refused")
	wantContains(t, structural, "<h3>Announcements", "the refused delete leaves the real folder intact")

	// Deleting the real folder empties the panel (the empty-state message appears).
	gone := deleteFolder(fid)
	wantNotContains(t, gone, "<h3>Announcements", "the deleted folder stops being listed")
	wantContains(t, gone, "No public folders", "the empty state shows after the last folder goes")
}

// TestPublicFoldersRequiresSystem proves an org admin cannot reach the page.
func TestPublicFoldersRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}},
		domains: []directory.DomainInfo{{ID: 1, Name: "local.test"}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/public-folders", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-admin public-folders page = %d, want 403", resp.StatusCode)
	}

	// The writes are refused too: a granted "anyone" right opens a folder to the
	// whole domain.
	for path, form := range map[string]url.Values{
		"/admin/ui/public-folders/folder": {"domain": {"local.test"}, "name": {"Leak"}},
		"/admin/ui/public-folders/grant": {"domain": {"local.test"}, "fid": {"5"}, "grantee": {"anyone"},
			"rights": {rightsValue(mapi.RightsOwner)}},
		"/admin/ui/public-folders/grant/remove":  {"domain": {"local.test"}, "fid": {"5"}, "memberID": {"0"}},
		"/admin/ui/public-folders/folder/delete": {"domain": {"local.test"}, "fid": {"5"}},
	} {
		wantStatus(t, htmxPOST(t, ts, path, session, csrf, form), http.StatusForbidden, "org-admin "+path)
	}
}
