package admin

import (
	"io"
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
	resp := authedGET(t, ts, "/admin/ui/domains", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("domains page status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hermex.test") || !strings.Contains(string(body), "Add domain") {
		t.Errorf("domains page missing content: %s", body)
	}
}

// TestUICreateDomain proves the form creates a domain and refreshes the panel.
func TestUICreateDomain(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/domains", session, csrf, url.Values{"name": {"new.test"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create domain status %d, want 200", resp.StatusCode)
	}
	if d.createdDomain != "new.test" {
		t.Errorf("created domain %q, want new.test", d.createdDomain)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `id="domains-panel"`) {
		t.Errorf("response is not the domains panel: %s", body)
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
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("purge status %d, want 200", resp.StatusCode)
	}
	if d.purgedDomain != 3 || !d.purgeFiles {
		t.Errorf("purge invoked id=%d files=%v, want 3/true", d.purgedDomain, d.purgeFiles)
	}
	if !strings.Contains(string(body), `id="domains-panel"`) {
		t.Errorf("response is not the domains panel: %s", body)
	}
}

// TestUIDomainsPageRequiresSystem proves the system gate covers the new pages.
func TestUIDomainsPageRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/domains", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin domains page = %d, want 403", resp.StatusCode)
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
	resp := authedGET(t, ts, "/admin/ui/domains/3", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("domain detail status %d, want 200", resp.StatusCode)
	}
	s := string(body)
	for _, want := range []string{"acme.test", "Suspended", "Maximum users", `value="25"`, "Usage", ">5<", "Org2"} {
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
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("domain save status %d, want 200", resp.StatusCode)
	}
	if d.updatedDomain != 3 || d.updateDomainArg.Status != 1 || d.updateDomainArg.MaxUser != 10 || d.updateDomainArg.Title != "X" {
		t.Errorf("update arg = %+v, want id 3 / status 1 / maxUser 10 / title X", d.updateDomainArg)
	}
	if d.assignDomainID != 3 || d.assignOrgID != 2 {
		t.Errorf("org assign = domain %d org %d, want 3/2", d.assignDomainID, d.assignOrgID)
	}
	if !strings.Contains(string(body), `class="ok"`) {
		t.Errorf("save response = %s, want a success acknowledgement", body)
	}
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
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin detail page = %d, want 403", resp.StatusCode)
	}
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
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("purge-from-detail status %d, want 200", resp.StatusCode)
	}
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
	resp := authedGET(t, ts, "/admin/ui/aliases", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("aliases page status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "sales@hermex.test") || !strings.Contains(string(body), "Add alias") {
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create alias status %d, want 200", resp.StatusCode)
	}
	if d.createdAlias != "sales@hermex.test" || d.createdAliasTo != "boss@hermex.test" {
		t.Errorf("created alias %q -> %q, want sales -> boss", d.createdAlias, d.createdAliasTo)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `id="aliases-panel"`) {
		t.Errorf("response is not the aliases panel: %s", body)
	}
}
