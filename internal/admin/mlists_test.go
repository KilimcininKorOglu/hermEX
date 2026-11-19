package admin

import (
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestUICreateMList proves the create form carries the address, type and
// privilege through to the directory and returns the refreshed panel.
func TestUICreateMList(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/mlists", session, csrf,
		url.Values{"listname": {"team@hermex.test"}, "type": {"2"}, "privilege": {"3"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create list status %d, want 200", resp.StatusCode)
	}
	if d.createdMList != "team@hermex.test" || d.createdMListType != 2 || d.createdMListPriv != 3 {
		t.Errorf("created list = %q type=%d priv=%d, want team@hermex.test type=2 priv=3",
			d.createdMList, d.createdMListType, d.createdMListPriv)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `id="mlists-panel"`) {
		t.Errorf("response is not the lists panel fragment: %s", body)
	}
}

// TestUICreateMListNeverReadsATypoAsAnOpenList forges a type and a privilege that are
// not numbers. Read as 0 they created an ordinary list anyone may post to; they must
// reach the directory as the refused -1.
func TestUICreateMListNeverReadsATypoAsAnOpenList(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	htmxPOST(t, ts, "/admin/ui/mlists", session, csrf,
		url.Values{"listname": {"team@hermex.test"}, "type": {"x"}, "privilege": {"internal"}}).Body.Close()
	if d.createdMListType != -1 || d.createdMListPriv != -1 {
		t.Errorf("forged type/privilege reached the directory as %d/%d, want -1/-1", d.createdMListType, d.createdMListPriv)
	}
}

// TestUIMListMembers proves the members textarea is parsed to addresses and
// written to the directory for the named list.
func TestUIMListMembers(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		mlists: []directory.MListInfo{{Listname: "team@hermex.test"}},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/mlists/team@hermex.test/members", session, csrf,
		url.Values{"members": {"alice@hermex.test\nbob@hermex.test"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set members status %d, want 200", resp.StatusCode)
	}
	if d.setMembersUser != "team@hermex.test" ||
		!slices.Equal(d.setMembers, []string{"alice@hermex.test", "bob@hermex.test"}) {
		t.Errorf("set members for %q = %v, want team@hermex.test [alice bob]", d.setMembersUser, d.setMembers)
	}
}

// TestUIMListOwner proves setting a list's owner from the management form reaches the
// directory (the Exchange managedBy attribute).
func TestUIMListOwner(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		mlists: []directory.MListInfo{{Listname: "team@hermex.test"}},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/mlists/team@hermex.test/owner", session, csrf,
		url.Values{"owner": {"alice@hermex.test"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set owner status %d, want 200", resp.StatusCode)
	}
	if d.setOwnerUser != "team@hermex.test" || d.setOwner != "alice@hermex.test" {
		t.Errorf("set owner for %q = %q, want team@hermex.test alice@hermex.test", d.setOwnerUser, d.setOwner)
	}
}

// TestUIDeleteMList proves deletion reaches the directory and redirects htmx back
// to the lists page.
func TestUIDeleteMList(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/mlists/team@hermex.test/delete", session, csrf, url.Values{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete list status %d, want 200", resp.StatusCode)
	}
	if d.deletedMList != "team@hermex.test" {
		t.Errorf("deleted %q, want team@hermex.test", d.deletedMList)
	}
	if loc := resp.Header.Get("HX-Redirect"); loc != "/admin/ui/mlists" {
		t.Errorf("HX-Redirect = %q, want /admin/ui/mlists", loc)
	}
}

// TestUIMListWritesNeedASystemAdmin proves a domain admin can neither create,
// change nor delete a distribution list through the panel forms, and none of
// those writes reaches the directory.
func TestUIMListWritesNeedASystemAdmin(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, perms: []directory.Permission{{Name: directory.PermDomainAdmin, Params: "*"}},
		mlists: []directory.MListInfo{{Listname: "team@hermex.test"}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	base := "/admin/ui/mlists/team@hermex.test"
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/mlists", session, csrf,
		url.Values{"listname": {"x@hermex.test"}, "type": {"0"}, "privilege": {"0"}}), http.StatusForbidden, "create")
	wantStatus(t, htmxPUT(t, ts, base+"/members", session, csrf, url.Values{"members": {"e@evil.test"}}), http.StatusForbidden, "members")
	wantStatus(t, htmxPUT(t, ts, base+"/specifieds", session, csrf, url.Values{"specifieds": {"e@evil.test"}}), http.StatusForbidden, "specifieds")
	wantStatus(t, htmxPUT(t, ts, base+"/owner", session, csrf, url.Values{"owner": {"e@evil.test"}}), http.StatusForbidden, "owner")
	wantStatus(t, htmxPOST(t, ts, base+"/delete", session, csrf, url.Values{}), http.StatusForbidden, "delete")
	if d.createdMList != "" || d.setMembersUser != "" || d.setOwnerUser != "" || d.deletedMList != "" {
		t.Errorf("a domain admin changed lists: created %q, members %q, owner %q, deleted %q",
			d.createdMList, d.setMembersUser, d.setOwnerUser, d.deletedMList)
	}
}

// TestUIMListDetailRenders proves the detail page renders the list with its
// privilege label and current members (and catches template syntax errors).
func TestUIMListDetailRenders(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		mlists:       []directory.MListInfo{{Listname: "team@hermex.test", ListType: 0, ListPriv: 1}},
		mlistMembers: []string{"alice@hermex.test"},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/mlists/team@hermex.test", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"team@hermex.test", "Members only", "alice@hermex.test"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail page missing %q", want)
		}
	}
}
