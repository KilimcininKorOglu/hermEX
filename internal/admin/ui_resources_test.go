package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestUIDomainsPage proves the domains page lists domains for a system admin.
func TestUIDomainsPage(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domains: []directory.DomainInfo{{ID: 1, Name: "hermex.test"}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	unauthReq := newRequest(t, "GET", ts.URL+"/admin/ui/domains", nil)
	unauthResp, err := noRedirectClient().Do(unauthReq)
	mustNoErr(t, err, "unauth domains")
	closeBody(t, unauthResp.Body)
	if unauthResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unauth domains = %d, want 303", unauthResp.StatusCode)
	}

	resp := authedGET(t, ts, "/admin/ui/domains", session)
	body := wantBody(t, resp, http.StatusOK, "domains page")
	if !strings.Contains(body, "hermex.test") || !strings.Contains(body, "Add domain") {
		t.Errorf("domains page missing content: %s", body)
	}
}

// TestUIDomainsPageNamesOrganization proves a domain row names the organization
// it belongs to rather than its numeric id, and says None for a domain outside
// every organization.
func TestUIDomainsPageNamesOrganization(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domains: []directory.DomainInfo{{ID: 1, Name: "acme.test", OrgID: 2}, {ID: 2, Name: "solo.test"}},
		orgs:    map[int64]directory.OrgInfo{2: {ID: 2, Name: "Acme Group"}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/domains", session)
	s := wantBody(t, resp, http.StatusOK, "domains page with org")
	if !strings.Contains(s, "<td>Acme Group</td>") {
		t.Errorf("acme.test row does not name its organization:\n%s", s)
	}
	if !strings.Contains(s, `<span class="muted">None</span>`) {
		t.Errorf("solo.test row does not say it has no organization:\n%s", s)
	}
}

// TestUICreateDomain proves the form creates a domain and refreshes the panel.
func TestUICreateDomain(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/domains", session, csrf, url.Values{"name": {"new.test"}})
	body := wantBody(t, resp, http.StatusOK, "create domain")
	if d.createdDomain != "new.test" {
		t.Errorf("created domain %q, want new.test", d.createdDomain)
	}
	if !strings.Contains(body, `id="domains-panel"`) {
		t.Errorf("response is not the domains panel: %s", body)
	}
}

// TestUICreateDomainWithMaxUser proves the create form applies the posted user
// limit to the new domain.
func TestUICreateDomainWithMaxUser(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/domains", session, csrf, url.Values{"name": {"new.test"}, "maxUser": {"25"}})
	wantStatus(t, resp, http.StatusOK, "create domain with maxUser")
	if d.createdDomain != "new.test" || d.updateDomainArg.MaxUser != 25 {
		t.Errorf("create+limit = %q / maxUser %d, want new.test / 25", d.createdDomain, d.updateDomainArg.MaxUser)
	}
}

// TestUIDomainsPagePrefillsMaxUser proves the create form pre-fills the system
// create-default for the user limit.
func TestUIDomainsPagePrefillsMaxUser(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		createDefaults: map[int64]directory.CreateDefaults{0: {Domain: directory.DomainCreateDefaults{MaxUser: 50}}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/domains", session)
	body := wantBody(t, resp, http.StatusOK, "domains page prefill")
	if !strings.Contains(body, `name="maxUser"`) || !strings.Contains(body, `value="50"`) {
		t.Errorf("domains page did not pre-fill maxUser=50:\n%s", body)
	}
}

// TestUIDomainPurge proves the domains page purge action purges the domain and
// passes the deleteFiles flag through, refreshing the panel.
func TestUIDomainPurge(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domains: []directory.DomainInfo{{ID: 3, Name: "acme.test"}},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/domains/3/purge", session, csrf, url.Values{"deleteFiles": {"true"}})
	body := wantBody(t, resp, http.StatusOK, "purge domain")
	if d.purgedDomain != 3 || !d.purgeFiles {
		t.Errorf("purge invoked id=%d files=%v, want 3/true", d.purgedDomain, d.purgeFiles)
	}
	if !strings.Contains(body, `id="domains-panel"`) {
		t.Errorf("response is not the domains panel: %s", body)
	}
}

// TestDomainPurgeReportsLeftoverFiles proves a purge whose files could not all be
// removed is shown as that, on the panel and on the API: reading it as a clean
// success hides mailboxes still on disk, and reading it as a failed purge sends
// the operator to retry a domain that is already gone.
func TestDomainPurgeReportsLeftoverFiles(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domains:  []directory.DomainInfo{{ID: 3, Name: "acme.test"}},
		purgeErr: fmt.Errorf("%w: remove /m/u: device busy", directory.ErrPurgeFilesRemain),
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/domains/3/purge", session, csrf, url.Values{"deleteFiles": {"true"}})
	body := wantBody(t, resp, http.StatusOK, "purge with leftover files")
	if !strings.Contains(body, "some of its mailbox files could not be removed") {
		t.Errorf("the panel does not report the leftover files:\n%s", body)
	}
	if strings.Contains(body, "Could not purge domain.") {
		t.Errorf("the panel reports a failed purge for a domain that is gone:\n%s", body)
	}
	if s := statusOf(authedReq(t, ts, "DELETE", "/admin/domains/3?deleteFiles=true", session, csrf, "")); s != http.StatusInternalServerError {
		t.Errorf("API purge with leftover files = %d, want 500", s)
	}
}

// TestUIDomainsPageRequiresSystem proves the system gate covers the new pages.
func TestUIDomainsPageRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	// The detail of a domain, its own included, carries system-wide settings (DKIM
	// keys, quotas, the organization), so a domain admin is refused it too.
	for _, path := range []string{"/admin/ui/domains", "/admin/ui/domains/1", "/admin/ui/domains/2"} {
		resp := authedGET(t, ts, path, session)
		wantStatus(t, resp, http.StatusForbidden, "domain-admin "+path)
	}
}

// TestUIDomainAndAliasWritesNeedASystemAdmin proves a domain admin can neither
// create, edit nor purge a domain, its own included, nor read or create aliases
// through the panel forms, and that a refused call reaches no directory write.
func TestUIDomainAndAliasWritesNeedASystemAdmin(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		aliases: []directory.AliasInfo{{ID: 1, Alias: "sales@other.test", Main: "boss@other.test"}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, htmxPOST(t, ts, "/admin/ui/domains", session, csrf, url.Values{"name": {"new.test"}}),
		http.StatusForbidden, "create domain")
	wantStatus(t, htmxPUT(t, ts, "/admin/ui/domains/1", session, csrf, url.Values{"status": {"0"}, "maxUser": {"0"}}),
		http.StatusForbidden, "save own domain")
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/domains/1/purge", session, csrf, url.Values{"deleteFiles": {"true"}}),
		http.StatusForbidden, "purge own domain")
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/aliases", session, csrf,
		url.Values{"alias": {"ceo@other.test"}, "main": {"x@hermex.test"}}), http.StatusForbidden, "create alias")
	page := wantBody(t, authedGET(t, ts, "/admin/ui/aliases", session), http.StatusForbidden, "aliases page")
	if strings.Contains(page, "sales@other.test") {
		t.Error("the refused aliases page lists an alias")
	}

	if d.createdDomain != "" || d.updatedDomain != 0 || d.purgedDomain != 0 || d.createdAlias != "" {
		t.Errorf("a refused caller wrote: domain %q, updated %d, purged %d, alias %q",
			d.createdDomain, d.updatedDomain, d.purgedDomain, d.createdAlias)
	}
}

// TestUIDomainDetailPage proves the domain detail page renders the editable
// fields, the status/organization selectors, and the user counts.
func TestUIDomainDetailPage(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 3, Name: "acme.test", OrgID: 2, Status: 1, MaxUser: 25, Title: "Acme", ActiveUsers: 5},
		orgs:         map[int64]directory.OrgInfo{1: {ID: 1, Name: "Org1"}, 2: {ID: 2, Name: "Org2"}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	unauthReq := newRequest(t, "GET", ts.URL+"/admin/ui/domains/3", nil)
	unauthResp, err := noRedirectClient().Do(unauthReq)
	mustNoErr(t, err, "unauth domain detail")
	closeBody(t, unauthResp.Body)
	if unauthResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unauth domain detail = %d, want 303", unauthResp.StatusCode)
	}

	resp := authedGET(t, ts, "/admin/ui/domains/3", session)
	s := wantBody(t, resp, http.StatusOK, "domain detail")
	for _, want := range []string{"acme.test", "Suspended", "Maximum users", `value="25"`, "Active users", ">5<", "Org2"} {
		if !strings.Contains(s, want) {
			t.Errorf("domain detail page missing %q:\n%s", want, s)
		}
	}
}

// TestUIDomainSave proves the detail form edits the domain and reassigns its
// organization, acknowledging success.
func TestUIDomainSave(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPUT(t, ts, "/admin/ui/domains/3", session, csrf, url.Values{
		"status": {"1"}, "maxUser": {"10"}, "title": {"X"}, "org": {"2"},
	})
	body := wantBody(t, resp, http.StatusOK, "domain save")
	if d.updatedDomain != 3 || d.updateDomainArg.Status != 1 || d.updateDomainArg.MaxUser != 10 || d.updateDomainArg.Title != "X" {
		t.Errorf("update arg = %+v, want id 3 / status 1 / maxUser 10 / title X", d.updateDomainArg)
	}
	if d.assignDomainID != 3 || d.assignOrgID != 2 {
		t.Errorf("org assign = domain %d org %d, want 3/2", d.assignDomainID, d.assignOrgID)
	}
	if !strings.Contains(body, `class="ok"`) {
		t.Errorf("save response = %s, want a success acknowledgement", body)
	}
}

// TestUIDomainFormsNeverReadATypoAsZero forges values that are not numbers. Read as
// 0 they reactivated a suspended domain, lifted its user limit and detached it from
// its organization; the edit must hand the directory -1 for each, and the create
// form must refuse a bad limit before any domain exists.
func TestUIDomainFormsNeverReadATypoAsZero(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	put := htmxPUT(t, ts, "/admin/ui/domains/3", session, csrf,
		url.Values{"status": {"off"}, "maxUser": {"1O"}, "org": {"x"}})
	wantStatus(t, put, http.StatusOK, "forged edit")
	if a := d.updateDomainArg; a.Status != -1 || a.MaxUser != -1 {
		t.Errorf("forged edit reached the directory as status %d maxUser %d, want -1/-1", a.Status, a.MaxUser)
	}
	if d.assignOrgID != -1 && d.assignDomainID == 3 {
		t.Errorf("forged org reached the directory as %d, want -1", d.assignOrgID)
	}

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/domains", session, csrf, url.Values{"name": {"new.test"}, "maxUser": {"-5"}}),
		http.StatusOK, "create with a negative limit")
	wantContains(t, body, "user limit must be", "the refusal")
	wantEq(t, d.createdDomain, "", "a domain created despite the refused limit")
}

// TestUIDomainDetailRequiresSystem proves the detail page is system-admin-only.
func TestUIDomainDetailRequiresSystem(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		domainDetail: directory.DomainDetail{ID: 3, Name: "acme.test"},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/domains/3", session)
	wantStatus(t, resp, http.StatusForbidden, "domain-admin detail page")
}

// TestUIDomainPurgeFromDetail proves purging from the detail page redirects back
// to the domains list (the domain no longer exists) rather than swapping a panel.
func TestUIDomainPurgeFromDetail(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/domains/3/purge", session, csrf, url.Values{
		"from": {"detail"}, "deleteFiles": {"true"},
	})
	wantStatus(t, resp, http.StatusOK, "purge-from-detail")
	if got := resp.Header.Get("HX-Redirect"); got != "/admin/ui/domains" {
		t.Errorf("HX-Redirect = %q, want /admin/ui/domains", got)
	}
	if d.purgedDomain != 3 || !d.purgeFiles {
		t.Errorf("purge invoked id=%d files=%v, want 3/true", d.purgedDomain, d.purgeFiles)
	}
}

// TestUIAliasesPage proves the aliases page lists aliases for a system admin.
func TestUIAliasesPage(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		aliases: []directory.AliasInfo{{ID: 1, Alias: "sales@hermex.test", Main: "boss@hermex.test"}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	unauthReq := newRequest(t, "GET", ts.URL+"/admin/ui/aliases", nil)
	unauthResp, err := noRedirectClient().Do(unauthReq)
	mustNoErr(t, err, "unauth aliases")
	closeBody(t, unauthResp.Body)
	if unauthResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unauth aliases = %d, want 303", unauthResp.StatusCode)
	}

	resp := authedGET(t, ts, "/admin/ui/aliases", session)
	body := wantBody(t, resp, http.StatusOK, "aliases page")
	if !strings.Contains(body, "sales@hermex.test") || !strings.Contains(body, "Add alias") {
		t.Errorf("aliases page missing content: %s", body)
	}
}

// TestUICreateAlias proves the form creates an alias and refreshes the panel.
func TestUICreateAlias(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/aliases", session, csrf,
		url.Values{"alias": {"sales@hermex.test"}, "main": {"boss@hermex.test"}})
	body := wantBody(t, resp, http.StatusOK, "create alias")
	if d.createdAlias != "sales@hermex.test" || d.createdAliasTo != "boss@hermex.test" {
		t.Errorf("created alias %q -> %q, want sales -> boss", d.createdAlias, d.createdAliasTo)
	}
	if !strings.Contains(body, `id="aliases-panel"`) {
		t.Errorf("response is not the aliases panel: %s", body)
	}
}
