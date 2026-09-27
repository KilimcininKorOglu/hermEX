package admin

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestSettingsPageRenders proves the unified Settings page renders for a system admin
// with its category tabs and a representative panel from each, every operator-tunable
// setting consolidated onto one page.
func TestSettingsPageRenders(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/settings", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings page = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	// The four category tabs.
	for _, tab := range []string{">Anti-spam<", ">Delivery<", ">Limits<", ">Retention<"} {
		if !strings.Contains(page, tab) {
			t.Errorf("settings page missing the %q tab:\n%s", tab, page)
		}
	}
	// A representative panel from each category renders on the one page.
	for _, marker := range []string{
		"Spam threshold",       // anti-spam scoring
		"Base retry backoff",   // delivery / relay
		"IMAP maximum literal", // limits
		"Verdicts to keep",     // retention
	} {
		if !strings.Contains(page, marker) {
			t.Errorf("settings page missing panel marker %q", marker)
		}
	}
}

// TestSettingsPageHoldsTheGatewayLockoutAndFetchPolicy proves the Settings page
// carries the outbound gateway, the login lockout and the fetch policy with their
// stored values. The sidebar links no other page that holds them, so without them
// here an operator could not reach these settings at all.
func TestSettingsPageHoldsTheGatewayLockoutAndFetchPolicy(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	d.gateways = map[string]directory.SMTPGateway{
		directory.GlobalGateway: {Enabled: true, Host: "relay.provider.test", Port: 2525, Encryption: directory.GatewaySTARTTLS},
	}
	d.loginLockout, d.loginLockoutFound = directory.LoginLockoutSettings{MaxFails: 7, WindowSeconds: 600, LockoutSeconds: 1200}, true
	d.fetchSettings, d.fetchSettingsFound = directory.FetchSettings{AllowInternalSources: true}, true
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/settings", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings page = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	for _, want := range []string{
		`id="gateway-panel"`, `value="relay.provider.test"`,
		`id="loginlockout-panel"`, `name="login_max_fails" value="7"`,
		`id="fetchpolicy-panel"`, `name="fetch_allow_internal" value="1" checked`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
}
