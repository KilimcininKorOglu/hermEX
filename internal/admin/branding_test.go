package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestUIDomainBrandingSave proves the domain-detail branding form carries the
// per-domain login fields through to the directory and acknowledges the save.
func TestUIDomainBrandingSave(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPUT(t, ts, "/admin/ui/domains/1/branding", session, csrf,
		url.Values{
			"app_name": {"Acme Mail"}, "primary_color": {"#ff0000"}, "tagline": {"Mail by Acme"},
			"logo_url": {""}, "footer_text": {""},
		})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui domain branding save status %d, want 200", resp.StatusCode)
	}
	if !d.brandingSet || d.branding.AppName != "Acme Mail" || d.branding.PrimaryColor != "#ff0000" || d.branding.Tagline != "Mail by Acme" {
		t.Errorf("save captured branding=%+v set=%v, want Acme Mail / #ff0000 / Mail by Acme", d.branding, d.brandingSet)
	}
	if !strings.Contains(string(body), `class="ok"`) {
		t.Errorf("save response = %s, want a success acknowledgement", body)
	}
}
