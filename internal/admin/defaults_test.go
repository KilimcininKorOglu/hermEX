package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestAdminDefaultsRoundTrip proves the JSON endpoint stores and reads back the
// system create-defaults.
func TestAdminDefaultsRoundTrip(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedReq(t, ts, "PUT", "/admin/defaults", session, csrf,
		`{"domain":{"maxUser":50},"user":{"lang":"tr","pop3_imap":true}}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set defaults status %d, want 204", resp.StatusCode)
	}
	if d.setCreateDefaultsScope != 0 || d.createDefaults[0].Domain.MaxUser != 50 {
		t.Errorf("stored defaults scope=%d maxUser=%d, want 0/50", d.setCreateDefaultsScope, d.createDefaults[0].Domain.MaxUser)
	}

	get := authedGET(t, ts, "/admin/defaults", session)
	body, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if !strings.Contains(string(body), `"maxUser":50`) || !strings.Contains(string(body), `"lang":"tr"`) {
		t.Errorf("get defaults body = %s, want the stored values", body)
	}
}

// TestAdminDomainDefaultsRoundTrip proves the per-domain override stores and reads
// back, and that an empty override clears the row (fall back to system).
func TestAdminDomainDefaultsRoundTrip(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 3, Name: "acme.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedReq(t, ts, "PUT", "/admin/domains/3/createdefaults", session, csrf, `{"lang":"tr","web":false}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set domain defaults status %d, want 204", resp.StatusCode)
	}
	got := d.createDefaults[3].User
	if got.Lang == nil || *got.Lang != "tr" || got.Web == nil || *got.Web {
		t.Errorf("stored override = %+v, want lang tr / web false", got)
	}

	get := authedGET(t, ts, "/admin/domains/3/createdefaults", session)
	body, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if !strings.Contains(string(body), `"web":false`) || !strings.Contains(string(body), `"lang":"tr"`) {
		t.Errorf("get override body = %s, want the stored values", body)
	}

	// An empty override clears the row.
	clr := authedReq(t, ts, "PUT", "/admin/domains/3/createdefaults", session, csrf, `{}`)
	clr.Body.Close()
	if clr.StatusCode != http.StatusNoContent {
		t.Fatalf("clear override status %d, want 204", clr.StatusCode)
	}
	if d.deletedCreateDefaultsScope != 3 {
		t.Errorf("empty override deleted scope %d, want 3", d.deletedCreateDefaultsScope)
	}
}

// TestUISaveDomainDefaults proves the detail-page override form stores the tri-state
// override (a set toggle, the rest inherited).
func TestUISaveDomainDefaults(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPUT(t, ts, "/admin/ui/domains/3/createdefaults", session, csrf,
		url.Values{"lang": {"tr"}, "web": {"0"}, "smtp": {""}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save override status %d, want 200", resp.StatusCode)
	}
	got := d.createDefaults[3].User
	if got.Lang == nil || *got.Lang != "tr" || got.Web == nil || *got.Web {
		t.Errorf("override = %+v, want lang tr / web false", got)
	}
	if got.SMTP != nil {
		t.Errorf("SMTP set to %v, want inherit (nil)", got.SMTP)
	}
	if !strings.Contains(string(body), `class="ok"`) {
		t.Errorf("save response = %s, want a success acknowledgement", body)
	}
}

// TestUIDomainDetailShowsOverride proves the override section pre-fills a stored
// per-domain toggle.
func TestUIDomainDetailShowsOverride(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail:   directory.DomainDetail{ID: 3, Name: "acme.test"},
		createDefaults: map[int64]directory.CreateDefaults{3: {User: directory.UserCreateDefaults{Web: new(false)}}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/domains/3", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("domain detail status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "User create defaults") ||
		!strings.Contains(string(body), `<option value="0" selected>No</option>`) {
		t.Errorf("override section did not reflect web=No:\n%s", body)
	}
}

// TestUIDefaultsPage proves the editor pre-fills the stored max-users default and
// the effective user defaults.
func TestUIDefaultsPage(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		createDefaults:        map[int64]directory.CreateDefaults{0: {Domain: directory.DomainCreateDefaults{MaxUser: 50}}},
		effectiveUserDefaults: directory.ResolvedUserDefaults{Lang: "tr", Web: true, StorageKB: 100 * 1024},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/defaults", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("defaults page status %d, want 200", resp.StatusCode)
	}
	s := string(body)
	for _, want := range []string{`name="maxUser"`, `value="50"`, `value="tr"`, `name="web" checked`, `value="100"`} {
		if !strings.Contains(s, want) {
			t.Errorf("defaults page missing %q:\n%s", want, s)
		}
	}
}

// TestUISaveDefaults proves the editor form stores a full create-defaults set: a
// checked toggle is true, an unchecked one false, and MiB quotas become KiB.
func TestUISaveDefaults(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPUT(t, ts, "/admin/ui/defaults", session, csrf, url.Values{
		"maxUser": {"50"}, "lang": {"tr"}, "pop3_imap": {"on"}, "web": {"on"}, "storagemb": {"100"},
	})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save defaults status %d, want 200", resp.StatusCode)
	}
	cd := d.createDefaults[0]
	if cd.Domain.MaxUser != 50 || cd.User.Lang == nil || *cd.User.Lang != "tr" {
		t.Errorf("stored maxUser/lang = %d / %v, want 50 / tr", cd.Domain.MaxUser, cd.User.Lang)
	}
	if cd.User.POP3IMAP == nil || !*cd.User.POP3IMAP || cd.User.SMTP == nil || *cd.User.SMTP {
		t.Errorf("toggles = POP3IMAP %v / SMTP %v, want true / false", cd.User.POP3IMAP, cd.User.SMTP)
	}
	if cd.User.StorageKB == nil || *cd.User.StorageKB != 100*1024 {
		t.Errorf("storage = %v, want 102400 KiB", cd.User.StorageKB)
	}
	if !strings.Contains(string(body), `class="ok"`) {
		t.Errorf("save response = %s, want a success acknowledgement", body)
	}
}
