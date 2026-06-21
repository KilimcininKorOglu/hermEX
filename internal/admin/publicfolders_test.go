package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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
	resp := authedGET(t, ts, "/admin/ui/public-folders", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "local.test") {
		t.Errorf("page missing the domain option:\n%s", body)
	}

	// Creating a folder provisions the store and shows the folder.
	resp = htmxPOST(t, ts, "/admin/ui/public-folders/folder", session, csrf,
		url.Values{"domain": {"local.test"}, "name": {"Announcements"}})
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	// Assert the folder HEADING, not the substring "Announcements" (which also
	// appears in the create form's placeholder).
	if !strings.Contains(string(body), "<h3>Announcements") {
		t.Fatalf("create did not list the folder:\n%s", body)
	}
	fid := pubFolderID(t, ts, session, "Announcements")

	// Grant "anyone" Reviewer; it appears relabelled as a grant ROW (not the
	// "anyone" placeholder text nor the "Reviewer" level-dropdown option).
	resp = htmxPOST(t, ts, "/admin/ui/public-folders/grant", session, csrf,
		url.Values{"domain": {"local.test"}, "fid": {strconv.FormatInt(fid, 10)}, "grantee": {"anyone"}, "rights": {rightsValue(mapi.RightsReviewer)}})
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<td>anyone</td><td>Reviewer</td>") {
		t.Errorf("anyone/Reviewer grant row not shown:\n%s", body)
	}

	// Grant the in-domain poster Author rights (asserted as a grant row).
	resp = htmxPOST(t, ts, "/admin/ui/public-folders/grant", session, csrf,
		url.Values{"domain": {"local.test"}, "fid": {strconv.FormatInt(fid, 10)}, "grantee": {"poster@local.test"}, "rights": {rightsValue(mapi.RightsAuthor)}})
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<td>poster@local.test</td><td>Author</td>") {
		t.Errorf("poster/Author grant row not shown:\n%s", body)
	}

	// A cross-domain grantee is rejected (the grant would be inert under tenant routing).
	resp = htmxPOST(t, ts, "/admin/ui/public-folders/grant", session, csrf,
		url.Values{"domain": {"local.test"}, "fid": {strconv.FormatInt(fid, 10)}, "grantee": {"intruder@other.test"}, "rights": {rightsValue(mapi.RightsReviewer)}})
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "user in this domain") {
		t.Errorf("cross-domain grantee not rejected:\n%s", body)
	}
	if strings.Contains(string(body), "<td>intruder@other.test</td>") {
		t.Errorf("cross-domain grantee was stored despite rejection:\n%s", body)
	}

	// A structural-folder delete (IPM_SUBTREE = 0x02) is refused, leaving the tree intact.
	resp = htmxPOST(t, ts, "/admin/ui/public-folders/folder/delete", session, csrf,
		url.Values{"domain": {"local.test"}, "fid": {strconv.FormatInt(int64(mapi.PublicFIDIPMSubtree), 10)}})
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "structural folder") {
		t.Errorf("structural delete not refused:\n%s", body)
	}
	if !strings.Contains(string(body), "<h3>Announcements") {
		t.Errorf("structural delete must leave the real folder intact:\n%s", body)
	}

	// Deleting the real folder empties the panel (the empty-state message appears).
	resp = htmxPOST(t, ts, "/admin/ui/public-folders/folder/delete", session, csrf,
		url.Values{"domain": {"local.test"}, "fid": {strconv.FormatInt(fid, 10)}})
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "<h3>Announcements") {
		t.Errorf("folder still listed after delete:\n%s", body)
	}
	if !strings.Contains(string(body), "No public folders") {
		t.Errorf("empty-state message not shown after deleting the only folder:\n%s", body)
	}
}

// TestPublicFoldersRequiresSystem proves an org admin cannot reach the page.
func TestPublicFoldersRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/public-folders", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-admin public-folders page = %d, want 403", resp.StatusCode)
	}
}
