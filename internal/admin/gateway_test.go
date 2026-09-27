package admin

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// gatewayForm is a complete gateway submission the tests vary one field of.
func gatewayForm() url.Values {
	return url.Values{
		"enabled": {"1"}, "host": {"smtp.provider.example"}, "port": {"587"},
		"encryption": {"starttls"}, "username": {"relay"}, "password": {"s3cret"},
	}
}

// gatewayAdmin builds a panel server with a system admin session and one domain.
func gatewayAdmin(t *testing.T) (*fakeDir, *httptest.Server, string, string) {
	t.Helper()
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "tenant.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	return d, ts, session, csrf
}

// TestSaveGatewayStoresTheGlobal proves the global form reaches the store.
func TestSaveGatewayStoresTheGlobal(t *testing.T) {
	d, ts, session, csrf := gatewayAdmin(t)

	htmxPOST(t, ts, "/admin/ui/antispam/gateway", session, csrf, gatewayForm()).Body.Close()

	got := d.gateways[directory.GlobalGateway]
	if got.Host != "smtp.provider.example" || got.Port != 587 || !got.Enabled {
		t.Errorf("stored gateway = %+v, want the submitted host, port and enabled flag", got)
	}
}

// TestSaveGatewayKeepsTheStoredPassword is the usability case that is also a security one:
// an empty password field must keep the stored credential, because the panel never shows it
// and re-typing it on every edit is what makes operators write it down somewhere.
func TestSaveGatewayKeepsTheStoredPassword(t *testing.T) {
	d, ts, session, csrf := gatewayAdmin(t)
	htmxPOST(t, ts, "/admin/ui/antispam/gateway", session, csrf, gatewayForm()).Body.Close()

	form := gatewayForm()
	form.Set("password", "")
	form.Set("host", "smtp2.provider.example")
	htmxPOST(t, ts, "/admin/ui/antispam/gateway", session, csrf, form).Body.Close()

	if got := d.gateways[directory.GlobalGateway]; got.Password != "s3cret" {
		t.Errorf("stored password = %q, want the previous one kept", got.Password)
	}
}

// TestGatewayPanelNeverShowsThePassword proves the credential does not travel back to the
// browser once stored.
func TestGatewayPanelNeverShowsThePassword(t *testing.T) {
	_, ts, session, csrf := gatewayAdmin(t)

	resp := htmxPOST(t, ts, "/admin/ui/antispam/gateway", session, csrf, gatewayForm())
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if strings.Contains(string(body), "s3cret") {
		t.Errorf("the panel echoed the gateway password back to the browser:\n%s", body)
	}
}

// TestDeleteGatewayRemovesTheGlobal proves the remove button clears the configuration.
func TestDeleteGatewayRemovesTheGlobal(t *testing.T) {
	d, ts, session, csrf := gatewayAdmin(t)
	htmxPOST(t, ts, "/admin/ui/antispam/gateway", session, csrf, gatewayForm()).Body.Close()

	htmxPOST(t, ts, "/admin/ui/antispam/gateway/delete", session, csrf, url.Values{}).Body.Close()

	if _, ok := d.gateways[directory.GlobalGateway]; ok {
		t.Error("the global gateway survived the delete")
	}
}

// TestSaveDomainGatewayStoresTheOverride proves a domain's form writes that domain's own
// gateway, keyed by its name rather than the global key.
func TestSaveDomainGatewayStoresTheOverride(t *testing.T) {
	d, ts, session, csrf := gatewayAdmin(t)

	htmxPOST(t, ts, "/admin/ui/domains/1/gateway", session, csrf, gatewayForm()).Body.Close()

	if _, ok := d.gateways["tenant.test"]; !ok {
		t.Errorf("no override stored for the domain, gateways = %+v", d.gateways)
	}
}

// TestSaveGatewayRefusesWhenTheStoredOneCannotBeRead proves a save stops when the
// stored gateway cannot be read. An empty password field keeps the stored password,
// so a save on a failed read stored an empty password in its place.
func TestSaveGatewayRefusesWhenTheStoredOneCannotBeRead(t *testing.T) {
	for path, key := range map[string]string{
		"/admin/ui/antispam/gateway":  directory.GlobalGateway,
		"/admin/ui/domains/1/gateway": "tenant.test",
	} {
		d, ts, session, csrf := gatewayAdmin(t)
		d.gateways = map[string]directory.SMTPGateway{key: {Host: "smtp.provider.example", Password: "s3cret"}}
		d.gatewayErr = errors.New("directory unreachable")
		form := gatewayForm()
		form.Set("password", "")

		body := wantBody(t, htmxPOST(t, ts, path, session, csrf, form), http.StatusOK, path)
		wantContains(t, body, "nothing was saved", path+": the refusal is reported")
		if got := d.gateways[key].Password; got != "s3cret" {
			t.Errorf("%s: stored password = %q, want the stored one untouched", path, got)
		}
	}
}

// TestGatewayFormIsHiddenWhenItCannotBeRead proves a page does not offer the
// gateway form built from a failed read. It showed the defaults, and saving them
// replaced the configured gateway.
func TestGatewayFormIsHiddenWhenItCannotBeRead(t *testing.T) {
	d, ts, session, _ := gatewayAdmin(t)
	d.gatewayErr = errors.New("directory unreachable")
	for _, path := range []string{"/admin/ui/settings", "/admin/ui/domains/1"} {
		page := wantBody(t, authedGET(t, ts, path, session), http.StatusOK, path)
		wantContains(t, page, "gateway. The form is hidden", path+": the failed read is reported")
		if strings.Contains(page, `name="host"`) {
			t.Errorf("%s: the gateway form is offered after a failed read", path)
		}
	}
}

// TestDeleteDomainGatewayRemovesTheOverride proves removing an override returns the domain
// to the global gateway.
func TestDeleteDomainGatewayRemovesTheOverride(t *testing.T) {
	d, ts, session, csrf := gatewayAdmin(t)
	htmxPOST(t, ts, "/admin/ui/domains/1/gateway", session, csrf, gatewayForm()).Body.Close()

	htmxPOST(t, ts, "/admin/ui/domains/1/gateway/delete", session, csrf, url.Values{}).Body.Close()

	if _, ok := d.gateways["tenant.test"]; ok {
		t.Error("the domain override survived the delete")
	}
}
