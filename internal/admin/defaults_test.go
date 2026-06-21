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
