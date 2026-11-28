package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestUIDomainSplitSave proves the split-domain form stores a valid host, refuses
// a malformed one without touching the stored value, and clears the split when
// the field is empty.
func TestUIDomainSplitSave(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	save := func(host string) string {
		t.Helper()
		return wantBody(t, htmxPUT(t, ts, "/admin/ui/domains/1/split", session, csrf, url.Values{"split_relay_host": {host}}),
			http.StatusOK, "save split "+host)
	}

	wantStatus(t, htmxPUT(t, ts, "/admin/ui/domains/1/split", "", csrf, url.Values{"split_relay_host": {"mx.x.test"}}),
		http.StatusUnauthorized, "unauth split save")
	wantEq(t, d.splitHost, "", "split host after unauth attempt")

	if body := save(" Legacy.Acme.test "); !strings.Contains(body, `class="ok"`) {
		t.Fatalf("save response = %s, want a success acknowledgement", body)
	}
	if d.splitHost != "legacy.acme.test" {
		t.Errorf("stored host = %q", d.splitHost)
	}
	if body := save("bad host;rm"); strings.Contains(body, `class="ok"`) {
		t.Errorf("a malformed host was accepted: %s", body)
	}
	if d.splitHost != "legacy.acme.test" {
		t.Errorf("a refused save changed the host to %q", d.splitHost)
	}
	wantContains(t, save(""), `class="ok"`, "clear split response")
	if d.splitHost != "" {
		t.Errorf("an empty field left the host %q", d.splitHost)
	}
}

// TestUIDomainSplitNeedsASystemAdmin proves a domain admin cannot point the domain's
// unknown recipients at another host: the split relay receives that mail.
func TestUIDomainSplitNeedsASystemAdmin(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	wantStatus(t, htmxPUT(t, ts, "/admin/ui/domains/1/split", session, csrf,
		url.Values{"split_relay_host": {"mx.attacker.test"}}), http.StatusForbidden, "domain-admin split")
	wantEq(t, d.splitHost, "", "the split host after the refused save")
}
