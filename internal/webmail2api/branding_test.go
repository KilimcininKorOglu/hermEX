package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"hermex/internal/buildinfo"
	"hermex/internal/directory"
)

// brandingAuth is a test authenticator (a StaticAccounts that also answers
// GetDomainBranding) so handleBranding can resolve per-domain branding.
type brandingAuth struct {
	directory.StaticAccounts
	branding map[string]directory.DomainBranding
}

func (b brandingAuth) GetDomainBranding(domain string) (directory.DomainBranding, bool, error) {
	d, ok := b.branding[domain]
	return d, ok, nil
}

// TestHandleBrandingPerDomain proves the unauthenticated branding endpoint serves a
// domain's own login branding when set, resolves an accessed subdomain host to its
// parent domain's branding, and serves the global default for an unknown host, so
// each tenant brands its own login screen by the address-bar hostname.
func TestHandleBrandingPerDomain(t *testing.T) {
	auth := brandingAuth{
		StaticAccounts: directory.StaticAccounts{},
		branding: map[string]directory.DomainBranding{
			"acme.test": {AppName: "Acme Mail", PrimaryColor: "#ff0000"},
		},
	}
	srv := NewServer(auth, directory.StaticAccounts{}, nil, "mail.hermex.test", []byte("s"), "", false)
	get := func(domain string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/branding?domain="+domain, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("branding(%q) bad json: %v", domain, err)
		}
		return out
	}
	if b := get("acme.test"); b["app_name"] != "Acme Mail" || b["primary_color"] != "#ff0000" {
		t.Errorf("acme branding = %v, want Acme Mail / #ff0000", b)
	}
	// An accessed hostname like mail.acme.test resolves to the acme.test domain.
	if b := get("mail.acme.test"); b["app_name"] != "Acme Mail" {
		t.Errorf("subdomain host app_name = %v, want Acme Mail (resolved to acme.test)", b["app_name"])
	}
	if b := get("other.test"); b["app_name"] != "hermEX" {
		t.Errorf("unknown-host app_name = %v, want the hermEX default", b["app_name"])
	}
}

// TestBrandingCarriesTheBuildStamp proves the login footer gets a real version to
// render, and that a tenant cannot overwrite it with a version it is not running.
func TestBrandingCarriesTheBuildStamp(t *testing.T) {
	old := buildinfo.Commit
	buildinfo.Commit = "abc1234-dirty"
	defer func() { buildinfo.Commit = old }()

	auth := brandingAuth{
		StaticAccounts: directory.StaticAccounts{},
		branding: map[string]directory.DomainBranding{
			"acme.test": {AppName: "Acme Mail", FooterText: "Acme"},
		},
	}
	srv := NewServer(auth, directory.StaticAccounts{}, nil, "mail.hermex.test", []byte("s"), "", false)
	get := func(domain string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/branding?domain="+domain, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("branding(%q) bad json: %v", domain, err)
		}
		return out
	}
	// A domain that customized nothing still gets the stamp, since the footer
	// renders it for every tenant.
	if b := get("other.test"); b["version"] != "abc1234-dirty" {
		t.Errorf("default version = %v, want the build stamp", b["version"])
	}
	// A branded domain gets its own footer text and the same stamp.
	b := get("acme.test")
	if b["footer_text"] != "Acme" {
		t.Errorf("footer_text = %v, want the tenant value", b["footer_text"])
	}
	if b["version"] != "abc1234-dirty" {
		t.Errorf("branded version = %v, want the build stamp, not a tenant value", b["version"])
	}
}
