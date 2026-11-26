package admin

import (
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
			"app_name": {"Acme Mail"}, "primary_color": {"#ff0000"}, "footer_text": {"Mail by Acme"},
			"logo_url": {""},
		})
	body := readBody(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui domain branding save status %d, want 200", resp.StatusCode)
	}
	want := directory.DomainBranding{AppName: "Acme Mail", PrimaryColor: "#ff0000", FooterText: "Mail by Acme"}
	if !d.brandingSet || d.branding != want {
		t.Errorf("save captured branding=%+v set=%v, want %+v", d.branding, d.brandingSet, want)
	}
	if !strings.Contains(string(body), `class="ok"`) {
		t.Errorf("save response = %s, want a success acknowledgement", body)
	}
}

// TestUIDomainBrandingRefusesABadValue proves the form refuses a value outside
// the branding shapes (directory.DomainBranding.Validate) with a message naming
// the rules, and stores nothing: the values are served on the unauthenticated
// login page.
func TestUIDomainBrandingRefusesABadValue(t *testing.T) {
	for _, form := range []url.Values{
		{"app_name": {"Acme"}, "logo_url": {"javascript:alert(1)"}},
		{"app_name": {"Acme"}, "primary_color": {"red;}</style><script>x</script>"}},
		{"app_name": {strings.Repeat("A", directory.MaxBrandingAppName+1)}},
	} {
		d := &fakeDir{
			authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
		}
		ts := adminServer(t, d)
		session, csrf := loginCookies(t, ts)
		body := wantBody(t, htmxPUT(t, ts, "/admin/ui/domains/1/branding", session, csrf, form), http.StatusOK, "save "+form.Encode()[:20])
		wantContains(t, body, "must be an https:// address", "the refusal message")
		wantFalse(t, d.brandingSet, "branding stored from "+form.Encode()[:20])
	}
}
