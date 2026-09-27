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
	"hermex/internal/logging"
)

// fakeSyncer is a scripted LDAPSyncer for the Directory Sync tests.
type fakeSyncer struct {
	users    []ldapauth.SyncedUser
	groups   []ldapauth.SyncedGroup
	contacts []ldapauth.SyncedContact
	err      error
}

func (f *fakeSyncer) Sync(directory.LDAPConfig) ([]ldapauth.SyncedUser, error) {
	return f.users, f.err
}
func (f *fakeSyncer) SyncGroups(directory.LDAPConfig) ([]ldapauth.SyncedGroup, error) {
	return f.groups, nil
}
func (f *fakeSyncer) SyncContacts(directory.LDAPConfig) ([]ldapauth.SyncedContact, error) {
	return f.contacts, nil
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

// TestUISaveLDAPSyncSettings proves the form persists the profile-field selection
// (enabled + attribute override) and the group-sync settings, so a panel-configured
// operator's choices reach the sync.
func TestUISaveLDAPSyncSettings(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/ldap", session, csrf, url.Values{
		"uri": {"ldap://x:389"}, "bind_dn": {"cn=svc"}, "base_dn": {"ou=p"}, "username_attr": {"mail"},
		"field_displayName_enabled": {"on"},
		"field_title_enabled":       {"on"},
		"field_title_attr":          {"jobTitle"},
		"syncgroups":                {"on"},
		"group_base_dn":             {"ou=groups,dc=x"},
		"group_filter":              {"(objectClass=group)"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status %d, want 200", resp.StatusCode)
	}
	got := d.ldap[0]
	if !got.SyncFields["displayName"].Enabled {
		t.Error("displayName should be enabled")
	}
	if f := got.SyncFields["title"]; !f.Enabled || f.Attr != "jobTitle" {
		t.Errorf("title field = %+v, want enabled with attr jobTitle", f)
	}
	if !got.SyncGroups || got.GroupBaseDN != "ou=groups,dc=x" || got.GroupFilter != "(objectClass=group)" {
		t.Errorf("group settings = syncGroups=%v base=%q filter=%q, want the form values",
			got.SyncGroups, got.GroupBaseDN, got.GroupFilter)
	}
}

// TestUISaveLDAPAliasAttribute proves the alias attribute persists, so a panel-configured
// alias sync reaches the downsync.
func TestUISaveLDAPAliasAttribute(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/ldap", session, csrf, url.Values{
		"uri": {"ldap://x:389"}, "bind_dn": {"cn=svc"}, "base_dn": {"ou=p"}, "username_attr": {"mail"},
		"alias_attr": {"proxyAddresses"},
	})
	resp.Body.Close()

	if got := d.ldap[0]; got.AliasAttr != "proxyAddresses" {
		t.Errorf("alias attribute = %q, want proxyAddresses", got.AliasAttr)
	}
}

// TestUISaveLDAPContactSettings proves the contact-sync settings persist, so an operator
// configuring contact sync in the panel reaches the sync with a filing domain.
func TestUISaveLDAPContactSettings(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/ldap", session, csrf, url.Values{
		"uri": {"ldap://x:389"}, "bind_dn": {"cn=svc"}, "base_dn": {"ou=p"}, "username_attr": {"mail"},
		"synccontacts":    {"on"},
		"contact_base_dn": {"ou=contacts,dc=x"},
		"contact_filter":  {"(objectClass=contact)"},
	})
	resp.Body.Close()

	got := d.ldap[0]
	if !got.SyncContacts || got.ContactBaseDN != "ou=contacts,dc=x" || got.ContactFilter != "(objectClass=contact)" {
		t.Errorf("contact settings = syncContacts=%v base=%q filter=%q, want the form values",
			got.SyncContacts, got.ContactBaseDN, got.ContactFilter)
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

// TestUISaveLDAPRefusesWhenTheStoredOneCannotBeRead proves a save stops when the
// stored configuration cannot be read. An empty bind password keeps the stored one,
// so the save stored an empty password in its place.
func TestUISaveLDAPRefusesWhenTheStoredOneCannotBeRead(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		ldap:     map[int64]directory.LDAPConfig{0: {URI: "old", BindPassword: "kept-secret"}},
		readErrs: map[string]error{"GetLDAPConfig": errReadFailed},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/ldap", session, csrf,
		url.Values{"uri": {"new"}, "starttls": {"on"}, "bind_password": {""}}), http.StatusOK, "save")
	wantContains(t, body, "nothing was saved", "the refused save is reported")
	if got := d.ldap[0]; got.BindPassword != "kept-secret" || got.URI != "old" {
		t.Errorf("a save after a failed read replaced the stored configuration: %+v", got)
	}
}

// TestUISyncLDAP proves the sync trigger enqueues an async task rather than
// syncing inline: the response acknowledges the queued task and nothing is
// upserted until the worker runs it.
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
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "queued") {
		t.Errorf("sync response should acknowledge the queued task: %s", body)
	}
	if len(d.upsertedUsers) != 0 {
		t.Errorf("enqueue must not sync inline, but upserted %v", d.upsertedUsers)
	}
	if len(d.tasks) != 1 || d.tasks[0].Type != "ldapsync" || d.tasks[0].Status != directory.TaskPending {
		t.Errorf("expected one pending ldapsync task, got %+v", d.tasks)
	}
}

// boundFakeDir returns a fake directory with two domains, each bound to one
// connection (binding 1 = a.test, binding 2 = b.test).
func boundFakeDir() *fakeDir {
	return &fakeDir{
		upsertNew: true,
		ldapConns: map[int64]directory.LDAPConnection{1: {ID: 1, Name: "hq", URI: "ldaps://x"}},
		ldapBindings: map[int64]directory.LDAPBinding{
			1: {ID: 1, ConnectionID: 1, DomainID: 1, Domain: "a.test"},
			2: {ID: 2, ConnectionID: 1, DomainID: 2, Domain: "b.test"},
		},
	}
}

// TestTaskWorkerLDAPSync proves the worker claims a pending ldapsync task with no
// binding named (a legacy task), runs every binding, each upserting only its own
// domain's entries, and records a done status with counts.
func TestTaskWorkerLDAPSync(t *testing.T) {
	d := boundFakeDir()
	syncer := &fakeSyncer{users: []ldapauth.SyncedUser{{Username: "a@a.test"}, {Username: "b@b.test"}}}
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.SetLDAPSyncer(syncer)

	id, err := d.CreateTask("ldapsync", "", "admin@test")
	if err != nil {
		t.Fatal(err)
	}
	ran, err := srv.runNextTask()
	if !ran || err != nil {
		t.Fatalf("runNextTask ran=%v err=%v, want it to run the task", ran, err)
	}
	if len(d.upsertedUsers) != 2 {
		t.Errorf("worker upserted %v, want both directory entries", d.upsertedUsers)
	}
	got, ok, _ := d.GetTask(id)
	if !ok || got.Status != directory.TaskDone ||
		!strings.Contains(got.Message, "a.test: Synced 2 directory entries: 1 created") ||
		!strings.Contains(got.Message, "b.test: Synced 2 directory entries: 1 created") {
		t.Errorf("task = %+v, want done with each binding's counts", got)
	}
	if ran, _ := srv.runNextTask(); ran {
		t.Errorf("a second runNextTask ran, want the queue empty")
	}
}

// TestTaskWorkerLDAPSyncOneBinding proves a task naming a binding syncs that binding
// alone.
func TestTaskWorkerLDAPSyncOneBinding(t *testing.T) {
	d := boundFakeDir()
	syncer := &fakeSyncer{users: []ldapauth.SyncedUser{{Username: "a@a.test"}, {Username: "b@b.test"}}}
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.SetLDAPSyncer(syncer)

	id, err := d.CreateTask("ldapsync", "2", "admin@test")
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := srv.runNextTask(); !ran || err != nil {
		t.Fatalf("runNextTask ran=%v err=%v, want it to run the task", ran, err)
	}
	if len(d.upsertedUsers) != 1 || d.upsertedUsers[0] != "b@b.test" {
		t.Errorf("worker upserted %v, want only the b.test account", d.upsertedUsers)
	}
	if got, _, _ := d.GetTask(id); got.Status != directory.TaskDone || strings.Contains(got.Message, "a.test") {
		t.Errorf("task = %+v, want done for b.test only", got)
	}
}

// TestTaskWorkerRecordsUnreadableBindings proves a sync whose bindings could not be
// read records the read failure rather than reporting that nothing is bound.
func TestTaskWorkerRecordsUnreadableBindings(t *testing.T) {
	d := &fakeDir{readErrs: map[string]error{"ListLDAPBindings": errReadFailed}}
	sink := &failCaptureSink{}
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.SetLDAPSyncer(&fakeSyncer{})
	srv.SetLogger(logging.New(sink))

	id, err := d.CreateTask("ldapsync", "", "admin@test")
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := srv.runNextTask(); !ran || err != nil {
		t.Fatalf("runNextTask ran=%v err=%v, want it to run the task", ran, err)
	}
	if got, ok, _ := d.GetTask(id); !ok || got.Status != directory.TaskFailed {
		t.Errorf("task = %+v, want failed", got)
	}
	if e, ok := sink.find("panel.fail"); !ok || !strings.Contains(e.Err, errReadFailed.Error()) {
		t.Errorf("the failed read was not recorded (event %+v)", e)
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
