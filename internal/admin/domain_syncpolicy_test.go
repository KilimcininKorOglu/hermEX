package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/easpolicy"
)

// TestAdminGetDomainSyncPolicy proves a system admin reads a domain's device-policy
// override, resolved from the route's domain id to the stored-by-name policy.
func TestAdminGetDomainSyncPolicy(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail:     directory.DomainDetail{ID: 1, Name: "acme.test"},
		domainSyncPolicy: easpolicy.Policy{"MinDevicePasswordLength": 8},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/domains/1/syncpolicy", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get domain sync policy status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "MinDevicePasswordLength") {
		t.Errorf("body = %s, want the policy field", body)
	}
}

// TestAdminSetDomainSyncPolicy proves a system admin writes a domain's override and
// that an unknown policy field is refused (it cannot be stored then dropped).
func TestAdminSetDomainSyncPolicy(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedReq(t, ts, "PUT", "/admin/domains/1/syncpolicy", session, csrf, `{"MinDevicePasswordLength":8}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set domain sync policy status %d, want 204", resp.StatusCode)
	}
	if d.setDomainSyncPolicyDomain != "acme.test" || d.domainSyncPolicy["MinDevicePasswordLength"] != 8 {
		t.Errorf("set captured domain=%q policy=%v, want acme.test / field 8", d.setDomainSyncPolicyDomain, d.domainSyncPolicy)
	}

	if s := statusOf(authedReq(t, ts, "PUT", "/admin/domains/1/syncpolicy", session, csrf, `{"BogusField":1}`)); s != http.StatusBadRequest {
		t.Errorf("unknown policy field = %d, want 400", s)
	}
}

// TestUIDomainSyncPolicySave proves the device-policy section on the domain detail
// page saves the override and acknowledges success.
func TestUIDomainSyncPolicySave(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPUT(t, ts, "/admin/ui/domains/1/syncpolicy", session, csrf,
		url.Values{"MinDevicePasswordLength": {"8"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui domain sync policy save status %d, want 200", resp.StatusCode)
	}
	if d.setDomainSyncPolicyDomain != "acme.test" || d.domainSyncPolicy["MinDevicePasswordLength"] != 8 {
		t.Errorf("save captured domain=%q policy=%v, want acme.test / field 8", d.setDomainSyncPolicyDomain, d.domainSyncPolicy)
	}
	if !strings.Contains(string(body), `class="ok"`) {
		t.Errorf("save response = %s, want a success acknowledgement", body)
	}
}
