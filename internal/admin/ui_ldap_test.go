package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/ldapauth"
)

// fakeSyncer is a scripted LDAPSyncer for the Directory Sync tests.
type fakeSyncer struct {
	users []ldapauth.SyncedUser
	err   error
}

func (f *fakeSyncer) Sync(directory.LDAPConfig) ([]ldapauth.SyncedUser, error) {
	return f.users, f.err
}

func adminServerWithSyncer(t *testing.T, d Directory, syncer LDAPSyncer) *httptest.Server {
	t.Helper()
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.SetLDAPSyncer(syncer)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestUILDAPPage proves the Directory Sync page shows the stored config and
// never leaks the bind password to the browser.
func TestUILDAPPage(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		ldap: map[int64]directory.LDAPConfig{0: {
			URI: "ldaps://dc.test:636", BindDN: "cn=svc", BindPassword: "topsecret",
			BaseDN: "ou=people", UsernameAttr: "mail",
		}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/ldap", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ldap page status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ldaps://dc.test:636") {
		t.Errorf("ldap page missing the stored URI: %s", body)
	}
	if strings.Contains(string(body), "topsecret") {
		t.Errorf("ldap page LEAKED the bind password to the browser")
	}
	if !strings.Contains(string(body), "(unchanged)") {
		t.Errorf("ldap page should mark the password as set: %s", body)
	}
}

// TestUISaveLDAP proves the form stores the configuration.
func TestUISaveLDAP(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/ldap", session, csrf, url.Values{
		"uri": {"ldap://x:389"}, "starttls": {"on"}, "bind_dn": {"cn=svc"},
		"bind_password": {"pw"}, "base_dn": {"ou=p"}, "username_attr": {"mail"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save ldap status %d, want 200", resp.StatusCode)
	}
	got := d.ldap[0]
	if got.URI != "ldap://x:389" || !got.StartTLS || got.BindPassword != "pw" || got.UsernameAttr != "mail" {
		t.Errorf("saved config = %+v, want the form values", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Configuration saved") {
		t.Errorf("save response missing the confirmation: %s", body)
	}
}

// TestUISaveLDAPPreservesPassword proves an empty bind password keeps the stored
// secret rather than blanking it.
func TestUISaveLDAPPreservesPassword(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		ldap: map[int64]directory.LDAPConfig{0: {URI: "old", BindPassword: "kept-secret"}},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/ldap", session, csrf,
		url.Values{"uri": {"new"}, "bind_password": {""}})
	resp.Body.Close()
	if got := d.ldap[0]; got.BindPassword != "kept-secret" {
		t.Errorf("empty password should preserve the stored secret, got %q", got.BindPassword)
	}
	if got := d.ldap[0]; got.URI != "new" {
		t.Errorf("URI should update, got %q", got.URI)
	}
}

// TestUISyncLDAP proves the sync trigger imports every returned directory entry.
func TestUISyncLDAP(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		ldap: map[int64]directory.LDAPConfig{0: {URI: "ldap://x"}}, upsertNew: true,
	}
	syncer := &fakeSyncer{users: []ldapauth.SyncedUser{{Username: "a@test"}, {Username: "b@test"}}}
	ts := adminServerWithSyncer(t, d, syncer)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/ldap/sync", session, csrf, url.Values{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sync status %d, want 200", resp.StatusCode)
	}
	if len(d.upsertedUsers) != 2 {
		t.Errorf("upserted %v, want both directory entries", d.upsertedUsers)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "2 created") {
		t.Errorf("sync response missing the counts: %s", body)
	}
}

// TestUILDAPSyncUnavailable proves the trigger reports gracefully when no syncer
// is wired.
func TestUILDAPSyncUnavailable(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d) // no syncer
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/ldap/sync", session, csrf, url.Values{})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "not available") {
		t.Errorf("sync should report unavailable when unwired: %s", body)
	}
	if len(d.upsertedUsers) != 0 {
		t.Errorf("an unavailable sync still upserted %v", d.upsertedUsers)
	}
}

// TestUILDAPRequiresSystem proves the Directory Sync page is system-admin only.
func TestUILDAPRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/ldap", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-admin ldap page = %d, want 403", resp.StatusCode)
	}
}
