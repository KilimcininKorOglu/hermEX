package admin

import (
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
	body := wantBody(t, resp, http.StatusOK, "create list")
	if d.createdMList != "team@hermex.test" || d.createdMListType != 2 || d.createdMListPriv != 3 {
		t.Errorf("created list = %q type=%d priv=%d, want team@hermex.test type=2 priv=3",
			d.createdMList, d.createdMListType, d.createdMListPriv)
	}
	if !strings.Contains(body, `id="mlists-panel"`) {
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

	wantStatus(t, htmxPOST(t, ts, "/admin/ui/mlists", session, csrf,
		url.Values{"listname": {"team@hermex.test"}, "type": {"x"}, "privilege": {"internal"}}),
		http.StatusOK, "forged typo")
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

	wantStatus(t, htmxPUT(t, ts, "/admin/ui/mlists/team@hermex.test/members", session, csrf,
		url.Values{"members": {"alice@hermex.test\nbob@hermex.test"}}), http.StatusOK, "set members")
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

	wantStatus(t, htmxPUT(t, ts, "/admin/ui/mlists/team@hermex.test/owner", session, csrf,
		url.Values{"owner": {"alice@hermex.test"}}), http.StatusOK, "set owner")
	if d.setOwnerUser != "team@hermex.test" || d.setOwner != "alice@hermex.test" {
		t.Errorf("set owner for %q = %q, want team@hermex.test alice@hermex.test", d.setOwnerUser, d.setOwner)
	}
}

// TestUIMListOwnerOutsideTheOrg proves an owner the directory refuses as outside
// the list's organization is named on the page, not reported as a save failure.
func TestUIMListOwnerOutsideTheOrg(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		mlists:    []directory.MListInfo{{Listname: "team@hermex.test"}},
		createErr: directory.ErrMListOwnerScope,
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/mlists/team@hermex.test/owner", session, csrf,
		url.Values{"owner": {"dave@partner.test"}})
	body := wantBody(t, resp, http.StatusOK, "owner outside org")
	if !strings.Contains(body, "organization") {
		t.Errorf("the refusal does not name the organization rule:\n%s", body)
	}
}

// TestUIDeleteMList proves deletion reaches the directory and redirects htmx back
// to the lists page.
func TestUIDeleteMList(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/mlists/team@hermex.test/delete", session, csrf, url.Values{})
	wantStatus(t, resp, http.StatusOK, "delete list")
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

	req := newRequest(t, "GET", ts.URL+"/admin/ui/mlists/team@hermex.test", nil)
	unauthResp, err := noRedirectClient().Do(req)
	mustNoErr(t, err, "unauth mlists request")
	wantStatus(t, unauthResp, http.StatusSeeOther, "unauthenticated mlists GET")

	resp := authedGET(t, ts, "/admin/ui/mlists/team@hermex.test", session)
	body := wantBody(t, resp, http.StatusOK, "detail status")
	for _, want := range []string{"team@hermex.test", "Members only", "alice@hermex.test"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
}
