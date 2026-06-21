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
