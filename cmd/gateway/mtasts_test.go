package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/config"
	"hermex/internal/directory"
)

// fakeMTASTSDir is a directory double for the MTA-STS policy handler.
type fakeMTASTSDir struct {
	settings    directory.MTASTSSettings
	found       bool
	domains     []directory.DomainInfo
	settingsErr error
	domainsErr  error
}

func (f *fakeMTASTSDir) GetMTASTSSettings() (directory.MTASTSSettings, bool, error) {
	return f.settings, f.found, f.settingsErr
}

func (f *fakeMTASTSDir) ListDomains() ([]directory.DomainInfo, error) {
	return f.domains, f.domainsErr
}

// proxied is a sentinel next-handler marking that a request fell through to proxying
// rather than being answered as an MTA-STS policy.
func proxied(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Proxied", "1")
	w.WriteHeader(http.StatusTeapot)
}

func doRequest(h http.Handler, host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestMTASTSServesPolicyForActiveDomain proves an enabled deployment serves a valid,
// exact policy for an active domain's policy host: the mx is the server hostname (the
// MX every domain routes to), the mode and max_age are the operator's, and the bytes
// are the RFC wire form a sender validates verbatim.
func TestMTASTSServesPolicyForActiveDomain(t *testing.T) {
	dir := &fakeMTASTSDir{
		settings: directory.MTASTSSettings{Enabled: true, Mode: "testing", MaxAge: 86400}, found: true,
		domains: []directory.DomainInfo{{Name: "tenant.com", Status: 0}},
	}
	h := withMTASTS(&config.Config{Hostname: "mail.hermex.test"}, dir, http.HandlerFunc(proxied))
	rec := doRequest(h, "mta-sts.tenant.com", mtastsPolicyPath)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("X-Proxied") != "" {
		t.Error("policy request was proxied; it must be answered locally")
	}
	want := "version: STSv1\r\nmode: testing\r\nmx: mail.hermex.test\r\nmax_age: 86400\r\n"
	if rec.Body.String() != want {
		t.Errorf("policy body =\n%q\nwant\n%q", rec.Body.String(), want)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

// TestMTASTSDisabledIs404 proves a deployment that has not enabled publishing serves
// no policy — a sender must see "no policy" (404), never a half-published one.
func TestMTASTSDisabledIs404(t *testing.T) {
	dir := &fakeMTASTSDir{
		settings: directory.MTASTSSettings{Enabled: false, Mode: "testing", MaxAge: 86400}, found: true,
		domains: []directory.DomainInfo{{Name: "tenant.com", Status: 0}},
	}
	h := withMTASTS(&config.Config{Hostname: "mail.hermex.test"}, dir, http.HandlerFunc(proxied))
	rec := doRequest(h, "mta-sts.tenant.com", mtastsPolicyPath)
	if rec.Code != http.StatusNotFound {
		t.Errorf("disabled status = %d, want 404", rec.Code)
	}
}

// TestMTASTSUnknownDomainIs404 proves a policy host for a domain this server does not
// serve (or a suspended one) gets no policy, so the server never vouches for a domain
// it does not host.
func TestMTASTSUnknownDomainIs404(t *testing.T) {
	dir := &fakeMTASTSDir{
		settings: directory.MTASTSSettings{Enabled: true, Mode: "testing", MaxAge: 86400}, found: true,
		domains: []directory.DomainInfo{{Name: "tenant.com", Status: 1}}, // suspended
	}
	h := withMTASTS(&config.Config{Hostname: "mail.hermex.test"}, dir, http.HandlerFunc(proxied))
	if rec := doRequest(h, "mta-sts.other.org", mtastsPolicyPath); rec.Code != http.StatusNotFound {
		t.Errorf("unknown domain status = %d, want 404", rec.Code)
	}
	if rec := doRequest(h, "mta-sts.tenant.com", mtastsPolicyPath); rec.Code != http.StatusNotFound {
		t.Errorf("suspended domain status = %d, want 404", rec.Code)
	}
}

// TestMTASTSDirectoryErrorIs500 proves a directory failure is a per-request 5xx, not a
// panic that takes the front door down — the same resilience the cert store holds.
func TestMTASTSDirectoryErrorIs500(t *testing.T) {
	dir := &fakeMTASTSDir{settingsErr: errors.New("db down")}
	h := withMTASTS(&config.Config{Hostname: "mail.hermex.test"}, dir, http.HandlerFunc(proxied))
	rec := doRequest(h, "mta-sts.tenant.com", mtastsPolicyPath)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("db-error status = %d, want 500", rec.Code)
	}
}

// TestMTASTSPassesThroughNonPolicyRequests proves only the policy host+path is
// intercepted: a normal host, or the policy host on a different path, is proxied so the
// front door's routing is untouched.
func TestMTASTSPassesThroughNonPolicyRequests(t *testing.T) {
	dir := &fakeMTASTSDir{
		settings: directory.MTASTSSettings{Enabled: true, Mode: "testing", MaxAge: 86400}, found: true,
		domains: []directory.DomainInfo{{Name: "tenant.com", Status: 0}},
	}
	h := withMTASTS(&config.Config{Hostname: "mail.hermex.test"}, dir, http.HandlerFunc(proxied))
	for _, tc := range []struct{ host, path string }{
		{"mail.hermex.test", mtastsPolicyPath}, // normal host, policy path → proxy
		{"mta-sts.tenant.com", "/ews/"},        // policy host, other path → proxy
	} {
		rec := doRequest(h, tc.host, tc.path)
		if rec.Header().Get("X-Proxied") != "1" {
			t.Errorf("request %s%s was not proxied (code %d)", tc.host, tc.path, rec.Code)
		}
	}
}
