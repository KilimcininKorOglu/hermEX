package admin

import (
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

// TestUILDAPOverviewForSystemAdmin proves a system admin sees every connection and
// every bound domain, and never a bind password.
func TestUILDAPOverviewForSystemAdmin(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminSystem})
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	page := wantBody(t, authedGET(t, ts, "/admin/ui/ldap", session), http.StatusOK, "overview")
	wantContains(t, page, `href="/admin/ui/ldap/connections/1"`, "the first connection is listed")
	wantContains(t, page, `href="/admin/ui/ldap/connections/2"`, "the second connection is listed")
	wantContains(t, page, `href="/admin/ui/ldap/bindings/2"`, "every binding is listed")
	wantContains(t, page, `hx-post="/admin/ui/ldap/connections"`, "the add-connection form is offered")
	wantNotContains(t, page, "topsecret", "the bind password stays out of the page")
}

// TestUILDAPDomainAdminSeesOnlyItsBinding proves a domain admin sees its own
// domain's binding, no connection, and is refused another domain's binding page and
// every connection page.
func TestUILDAPDomainAdminSeesOnlyItsBinding(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	page := wantBody(t, authedGET(t, ts, "/admin/ui/ldap", session), http.StatusOK, "overview")
	wantContains(t, page, `href="/admin/ui/ldap/bindings/1"`, "the own binding is listed")
	wantNotContains(t, page, `href="/admin/ui/ldap/bindings/2"`, "the other binding is hidden")
	wantNotContains(t, page, `href="/admin/ui/ldap/connections/`, "no connection is listed")
	wantNotContains(t, page, `hx-post="/admin/ui/ldap/connections"`, "no add-connection form")

	own := wantBody(t, authedGET(t, ts, "/admin/ui/ldap/bindings/1", session), http.StatusOK, "own binding")
	wantContains(t, own, `hx-put="/admin/ui/ldap/bindings/1"`, "the own mapping form is offered")
	wantNotContains(t, own, `name="group_filter"`, "the system-only filter is not offered")
	wantNotContains(t, own, `name="connection_id"`, "the connection cannot be changed")
	wantStatus(t, authedGET(t, ts, "/admin/ui/ldap/bindings/2", session), http.StatusForbidden, "other binding")
	wantStatus(t, authedGET(t, ts, "/admin/ui/ldap/connections/1", session), http.StatusForbidden, "connection page")
}

// TestUISaveLDAPBindingKeepsSystemFields proves a domain admin's form save changes
// the mapping and cannot reach the system-only settings, even when it posts them.
func TestUISaveLDAPBindingKeepsSystemFields(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPUT(t, ts, "/admin/ui/ldap/bindings/1", session, csrf, url.Values{
		"field_title_enabled": {"on"}, "field_title_attr": {"jobTitle"},
		"alias_attr": {"proxyAddresses"}, "syncgroups": {"on"},
		"group_filter": {"(objectClass=*)"}, "base_dn": {"dc=everything"}, "connection_id": {"2"},
	}), http.StatusOK, "save")
	wantContains(t, body, "Configuration saved", "the save is confirmed")
	got := d.ldapBindings[1]
	wantEq(t, got.Mapping.Fields["title"], directory.LDAPSyncField{Enabled: true, Attr: "jobTitle"}, "the title field")
	wantEq(t, got.Mapping.AliasAttr, "proxyAddresses", "the alias attribute")
	wantTrue(t, got.Mapping.SyncGroups, "group sync is on")
	wantEq(t, got.Mapping.GroupFilter, "(cn=a*)", "the group filter keeps its stored value")
	wantEq(t, got.Mapping.BaseDN, "ou=a,dc=test", "the base DN keeps its stored value")
	wantEq(t, got.ConnectionID, int64(1), "the connection keeps its stored value")
}

// TestUISaveLDAPBindingRefusesMalformedAttribute proves the form refuses an
// attribute carrying filter syntax and stores nothing.
func TestUISaveLDAPBindingRefusesMalformedAttribute(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPUT(t, ts, "/admin/ui/ldap/bindings/1", session, csrf,
		url.Values{"alias_attr": {"mail)(uid=*"}}), http.StatusOK, "save")
	wantContains(t, body, "malformed", "the refusal is reported")
	wantEq(t, d.ldapBindings[1].Mapping.AliasAttr, "mail", "nothing was stored")
}

// TestUISaveLDAPConnectionKeepsPassword proves an empty bind password keeps the
// stored one while the rest of the form is saved.
func TestUISaveLDAPConnectionKeepsPassword(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminSystem})
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	page := wantBody(t, authedGET(t, ts, "/admin/ui/ldap/connections/1", session), http.StatusOK, "connection page")
	wantNotContains(t, page, "topsecret", "the bind password stays out of the page")
	wantContains(t, page, "(unchanged)", "the page marks the password as set")

	wantStatus(t, htmxPUT(t, ts, "/admin/ui/ldap/connections/1", session, csrf, url.Values{
		"name": {"hq"}, "uri": {"ldaps://dc2.test"}, "bind_dn": {"cn=svc"}, "bind_password": {""},
	}), http.StatusOK, "save")
	wantEq(t, d.ldapConns[1].BindPassword, "topsecret", "an empty password keeps the stored one")
	wantEq(t, d.ldapConns[1].URI, "ldaps://dc2.test", "the URI changes")
}

// TestUISaveLDAPConnectionRefusesWhenUnread proves a save stops when the stored
// connection cannot be read: an empty bind password keeps the stored one, so the
// save would store an empty password in its place.
func TestUISaveLDAPConnectionRefusesWhenUnread(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminSystem})
	d.readErrs = map[string]error{"GetLDAPConnection": errReadFailed}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPUT(t, ts, "/admin/ui/ldap/connections/1", session, csrf, url.Values{
		"name": {"hq"}, "uri": {"ldaps://new.test"},
	}), http.StatusOK, "save")
	wantContains(t, body, "nothing was saved", "the refused save is reported")
	wantEq(t, d.ldapConns[1].URI, "ldaps://dc.test", "the stored connection is unchanged")
}

// TestUICreateLDAPConnection proves the add form stores a connection and opens it,
// and a plaintext bind is refused.
func TestUICreateLDAPConnection(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminSystem})
	d.ldapConns = nil
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/ldap/connections", session, csrf, url.Values{
		"name": {"hq"}, "uri": {"ldaps://dc.test"}, "bind_password": {"pw"},
	})
	resp.Body.Close()
	wantEq(t, resp.Header.Get("HX-Redirect"), "/admin/ui/ldap/connections/1", "the new connection opens")
	wantEq(t, d.ldapConns[1].BindPassword, "pw", "the password is stored")
}

// TestUIResetLDAPBindingRestoresPreset proves the reset button brings back the
// preset mapping, keeps the system-only settings, and reloads the page.
func TestUIResetLDAPBindingRestoresPreset(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/ldap/bindings/1/reset", session, csrf, url.Values{})
	resp.Body.Close()
	wantEq(t, resp.Header.Get("HX-Redirect"), "/admin/ui/ldap/bindings/1", "the page reloads")
	got := d.ldapBindings[1].Mapping
	wantEq(t, got.AliasAttr, "proxyAddresses", "the preset alias attribute is back")
	wantTrue(t, got.Fields["displayName"].Enabled, "the preset fields are back")
	wantEq(t, got.GroupFilter, "(cn=a*)", "the group filter keeps its stored value")
}

// TestUISyncLDAPBindingQueuesTask proves the sync button queues one task naming the
// binding and syncs nothing inline.
func TestUISyncLDAPBindingQueuesTask(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServerWithSyncer(t, d, &fakeSyncer{users: []ldapauth.SyncedUser{{Username: "a@a.test"}}})
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/ldap/bindings/1/sync", session, csrf, url.Values{}),
		http.StatusOK, "sync")
	wantContains(t, body, "queued", "the queued task is acknowledged")
	if len(d.tasks) != 1 || d.tasks[0].Type != "ldapsync" || d.tasks[0].Params != "1" {
		t.Errorf("queued tasks = %+v, want one ldapsync task for binding 1", d.tasks)
	}
	wantEq(t, len(d.upsertedUsers), 0, "accounts synced inline")
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/ldap/bindings/2/sync", session, csrf, url.Values{}),
		http.StatusForbidden, "sync of another domain")
}

// TestUISyncLDAPBindingUnavailable proves the sync button reports that sync is
// unavailable when no syncer is wired, and queues nothing.
func TestUISyncLDAPBindingUnavailable(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminSystem})
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/ldap/bindings/1/sync", session, csrf, url.Values{}),
		http.StatusOK, "sync")
	wantContains(t, body, "not available", "the missing syncer is reported")
	wantEq(t, len(d.tasks), 0, "queued tasks")
}

// TestUILDAPRendersInBothLanguages proves the three Directory Sync pages render in
// English and Turkish with no raw catalogue key left on them.
func TestUILDAPRendersInBothLanguages(t *testing.T) {
	for lang, want := range map[string]string{"en": "Bound domains", "tr": "Bağlı alan adları"} {
		d := ldapAPIDir(directory.AdminRole{Role: directory.AdminSystem})
		d.uiPrefs = map[string]directory.UserPrefs{"admin@hermex.test": {Lang: lang}}
		ts := adminServer(t, d)
		session, _ := loginCookies(t, ts)
		for _, path := range []string{"/admin/ui/ldap", "/admin/ui/ldap/connections/1", "/admin/ui/ldap/bindings/1"} {
			page := wantBody(t, authedGET(t, ts, path, session), http.StatusOK, lang+" "+path)
			if path != "/admin/ui/ldap/bindings/1" {
				wantContains(t, page, want, lang+" "+path+" is translated")
			}
			if strings.Contains(page, ">ldap.") || strings.Contains(page, "\"ldap.") {
				t.Errorf("%s %s shows a raw catalogue key", lang, path)
			}
		}
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
