package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/logging"
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
	h := withMTASTS(&config.Config{Hostname: "mail.hermex.test"}, dir, nil, http.HandlerFunc(proxied))
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
	// Every fetch must reach this server, or a sender re-fetching after an id
	// change could be answered with the old policy by a cache in between.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
}

// TestMTASTSDisabledIs404 proves a deployment that has not enabled publishing serves
// no policy, a sender must see "no policy" (404), never a half-published one.
func TestMTASTSDisabledIs404(t *testing.T) {
	dir := &fakeMTASTSDir{
		settings: directory.MTASTSSettings{Enabled: false, Mode: "testing", MaxAge: 86400}, found: true,
		domains: []directory.DomainInfo{{Name: "tenant.com", Status: 0}},
	}
	h := withMTASTS(&config.Config{Hostname: "mail.hermex.test"}, dir, nil, http.HandlerFunc(proxied))
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
	h := withMTASTS(&config.Config{Hostname: "mail.hermex.test"}, dir, nil, http.HandlerFunc(proxied))
	if rec := doRequest(h, "mta-sts.other.org", mtastsPolicyPath); rec.Code != http.StatusNotFound {
		t.Errorf("unknown domain status = %d, want 404", rec.Code)
	}
	if rec := doRequest(h, "mta-sts.tenant.com", mtastsPolicyPath); rec.Code != http.StatusNotFound {
		t.Errorf("suspended domain status = %d, want 404", rec.Code)
	}
}

// eventSink collects the events the handler emits.
type eventSink struct{ events []logging.Event }

func (s *eventSink) Write(e logging.Event) { s.events = append(s.events, e) }

// TestMTASTSDirectoryErrorIs500 proves a failure of either directory read is a
// per-request 5xx, not a panic that takes the front door down, that the body
// carries none of the error's text, and that the failure is recorded: the
// operator's log is the only place a sender's missing policy can be traced.
func TestMTASTSDirectoryErrorIs500(t *testing.T) {
	for name, dir := range map[string]*fakeMTASTSDir{
		"settings": {settingsErr: errors.New("db down: settings")},
		"domains": {settings: directory.MTASTSSettings{Enabled: true, Mode: "testing", MaxAge: 86400},
			found: true, domainsErr: errors.New("db down: domains")},
	} {
		sink := &eventSink{}
		h := withMTASTS(&config.Config{Hostname: "mail.hermex.test"}, dir, logging.New(sink), http.HandlerFunc(proxied))
		req := httptest.NewRequest(http.MethodGet, "http://mta-sts.tenant.com"+mtastsPolicyPath, nil)
		req.RemoteAddr = "192.0.2.7:4711"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "db down") {
			t.Errorf("%s: the response leaks the error: %q", name, rec.Body.String())
		}
		if len(sink.events) != 1 || sink.events[0].Err != "db down: "+name || sink.events[0].RemoteAddr != "192.0.2.7:4711" {
			t.Errorf("%s: events = %+v, want one carrying the error and the client address", name, sink.events)
		}
	}
}

// TestMTASTSPolicyHostIgnoresCaseAndPort proves the policy host is matched as the
// DNS name it is (case-insensitive, RFC 4343) and that a port in the Host header
// does not hide it.
func TestMTASTSPolicyHostIgnoresCaseAndPort(t *testing.T) {
	dir := &fakeMTASTSDir{
		settings: directory.MTASTSSettings{Enabled: true, Mode: "enforce", MaxAge: 604800}, found: true,
		domains: []directory.DomainInfo{{Name: "tenant.com", Status: 0}},
	}
	h := withMTASTS(&config.Config{Hostname: "mail.hermex.test"}, dir, nil, http.HandlerFunc(proxied))
	rec := doRequest(h, "MTA-STS.Tenant.COM:443", mtastsPolicyPath)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "mode: enforce\r\n") {
		t.Errorf("status = %d body = %q, want the enforce policy", rec.Code, rec.Body.String())
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
	h := withMTASTS(&config.Config{Hostname: "mail.hermex.test"}, dir, nil, http.HandlerFunc(proxied))
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
