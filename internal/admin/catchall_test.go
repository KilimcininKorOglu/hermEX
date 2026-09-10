package admin

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"hermex/internal/directory"
)

// catchAllAdmin builds a panel server with a system admin session and one domain, which is
// what the catch-all form is rendered for.
func catchAllAdmin(t *testing.T) (*fakeDir, *httptest.Server, string, string) {
	t.Helper()
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "tenant.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	return d, ts, session, csrf
}

// TestSaveDomainCatchAllStoresTheAccount proves the form names the mailbox a domain
// collects unknown recipients in.
func TestSaveDomainCatchAllStoresTheAccount(t *testing.T) {
	d, ts, session, csrf := catchAllAdmin(t)

	htmxPUT(t, ts, "/admin/ui/domains/1/catchall", session, csrf,
		url.Values{"catchall": {"alice@tenant.test"}}).Body.Close()

	if got := d.catchAll["tenant.test"]; got != "alice@tenant.test" {
		t.Errorf("stored catch-all = %q, want alice@tenant.test", got)
	}
}

// TestSaveDomainCatchAllClears proves selecting none returns the domain to refusing
// unknown recipients.
func TestSaveDomainCatchAllClears(t *testing.T) {
	d, ts, session, csrf := catchAllAdmin(t)
	htmxPUT(t, ts, "/admin/ui/domains/1/catchall", session, csrf,
		url.Values{"catchall": {"alice@tenant.test"}}).Body.Close()

	htmxPUT(t, ts, "/admin/ui/domains/1/catchall", session, csrf, url.Values{"catchall": {""}}).Body.Close()

	if _, ok := d.catchAll["tenant.test"]; ok {
		t.Error("the catch-all survived the clear")
	}
}
