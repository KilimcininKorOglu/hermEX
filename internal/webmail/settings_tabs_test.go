package webmail

import (
	"net/http"
	"strings"
	"testing"
)

// TestSettingsPageIsOnePageWithTabs proves the settings page consolidates every
// section onto one page as tabs: a single GET /settings carries the general,
// inbox-rules, out-of-office, certificates, and password sections together, with
// a tab button per section and the requested tab rendered active.
func TestSettingsPageIsOnePageWithTabs(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	code, body := get(t, c, ts.URL+"/settings")
	if code != 200 {
		t.Fatalf("GET /settings = %d", code)
	}
	for _, tab := range []string{"general", "rules", "oof", "smime", "password"} {
		if !strings.Contains(body, `data-tab="`+tab+`"`) {
			t.Errorf("settings page missing the %q tab", tab)
		}
	}
	// Each section's own content is present on the one page (the consolidation:
	// these used to be five separate pages reached from five separate links).
	for _, marker := range []string{"Default compose format", "Inbox rules", "Out of office", "S/MIME certificates", "Change password"} {
		if !strings.Contains(body, marker) {
			t.Errorf("settings page missing the %q section", marker)
		}
	}
	if !strings.Contains(body, `class="settings-panel active" data-tab="general"`) {
		t.Errorf("the general tab is not active by default")
	}
}

// TestSettingsTabSelection proves ?tab= opens the requested section, so a form's
// post-redirect (e.g. to /settings?tab=oof) lands the user back on its own tab.
func TestSettingsTabSelection(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	for _, tab := range []string{"oof", "smime", "rules", "password"} {
		code, body := get(t, c, ts.URL+"/settings?tab="+tab)
		if code != 200 {
			t.Fatalf("GET /settings?tab=%s = %d", tab, code)
		}
		if !strings.Contains(body, `class="settings-panel active" data-tab="`+tab+`"`) {
			t.Errorf("tab %q is not active when requested", tab)
		}
	}
}

// TestSettingsSubPagesRedirect proves the former standalone settings URLs now
// redirect into the unified page on their own tab, so old bookmarks still work
// while the POST endpoints (which the forms target) keep serving.
func TestSettingsSubPagesRedirect(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	cases := map[string]string{
		"/oof":      "/settings?tab=oof",
		"/rules":    "/settings?tab=rules",
		"/smime":    "/settings?tab=smime",
		"/password": "/settings?tab=password",
	}
	for from, want := range cases {
		resp, err := c.Get(ts.URL + from)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("GET %s = %d, want 303", from, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != want {
			t.Errorf("GET %s redirects to %q, want %q", from, loc, want)
		}
	}
}

// TestMailToolbarConsolidatesSettings proves the mail toolbar no longer carries a
// separate link per settings page: one "Settings" link replaces the former Rules
// / Out of office / Certificates / Change password row (Compose and Import, which
// are actions rather than settings, stay).
func TestMailToolbarConsolidatesSettings(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	code, body := get(t, c, ts.URL+"/mail")
	if code != 200 {
		t.Fatalf("GET /mail = %d", code)
	}
	if !strings.Contains(body, `href="/settings"`) {
		t.Errorf("toolbar lost its Settings link")
	}
	for _, gone := range []string{`href="/rules"`, `href="/oof"`, `href="/smime"`, `href="/password"`} {
		if strings.Contains(body, gone) {
			t.Errorf("toolbar still carries a separate settings link %s; it should live under /settings", gone)
		}
	}
	for _, keep := range []string{`href="/compose"`, `href="/import"`} {
		if !strings.Contains(body, keep) {
			t.Errorf("toolbar lost the %s action", keep)
		}
	}
}

// TestSmimeErrorRendersSettingsPage proves an S/MIME validation error re-renders
// the whole settings page on the certificates tab with the message shown, rather
// than a bare error page — the free-text error cannot ride a post-redirect, so
// this is the one path that renders the unified page directly (with a 400).
func TestSmimeErrorRendersSettingsPage(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// Unlocking with no identity stored is a validation error.
	code, body := postMultipart(t, c, ts.URL+"/smime", map[string]string{"action": "unlock", "passphrase": "x"}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("unlock without an identity = %d, want 400", code)
	}
	if !strings.Contains(body, `class="settings-panel active" data-tab="smime"`) {
		t.Errorf("the error did not re-render the settings page on the certificates tab")
	}
	if !strings.Contains(body, "There is no certificate to unlock.") {
		t.Errorf("the error message is missing from the re-rendered page")
	}
}
