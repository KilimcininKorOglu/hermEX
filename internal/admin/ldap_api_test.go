package admin

import (
	"net/http"
	"testing"

	"hermex/internal/directory"
)

// ldapAPIDir returns a fake directory holding one connection (with a stored bind
// password) and two bound domains: binding 1 = a.test (domain 1), binding 2 =
// b.test (domain 2). Binding 1 carries system-only settings and the ad preset.
func ldapAPIDir(roles ...directory.AdminRole) *fakeDir {
	return &fakeDir{
		authOK: true, uid: 7, roles: roles,
		ldapConns: map[int64]directory.LDAPConnection{
			1: {ID: 1, Name: "hq", URI: "ldaps://dc.test", BindDN: "cn=svc", BindPassword: "topsecret"},
			2: {ID: 2, Name: "branch", URI: "ldaps://branch.test"},
		},
		ldapBindings: map[int64]directory.LDAPBinding{
			1: {ID: 1, ConnectionID: 1, DomainID: 1, Domain: "a.test", Preset: directory.LDAPPresetAD,
				Mapping: directory.LDAPMapping{BaseDN: "ou=a,dc=test", GroupFilter: "(cn=a*)", AliasAttr: "mail"}},
			2: {ID: 2, ConnectionID: 1, DomainID: 2, Domain: "b.test"},
		},
	}
}

// TestLDAPBindingDomainScope proves a domain admin reads and edits its own domain's
// binding and is refused another domain's, on read and on write.
func TestLDAPBindingDomainScope(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedGET(t, ts, "/admin/ldap/bindings/1", session), http.StatusOK, "own binding read")
	wantStatus(t, authedGET(t, ts, "/admin/ldap/bindings/2", session), http.StatusForbidden, "other binding read")
	wantStatus(t, authedPUT(t, ts, "/admin/ldap/bindings/2", session, csrf, `{}`),
		http.StatusForbidden, "other binding edit")
	wantStatus(t, authedPUT(t, ts, "/admin/ldap/bindings/1", session, csrf, `{"Mapping":{"aliasAttr":"proxyAddresses"}}`),
		http.StatusNoContent, "own binding edit")
	list := wantBody(t, authedGET(t, ts, "/admin/ldap/bindings", session), http.StatusOK, "binding list")
	wantContains(t, list, "a.test", "the list carries the own binding")
	wantNotContains(t, list, "b.test", "the list hides the other binding")
}

// TestLDAPBindingOrgScope proves an organization admin edits the binding of a domain
// in its organization and is refused one in another organization.
func TestLDAPBindingOrgScope(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminOrg, ScopeID: 5})
	d.domainDetail = directory.DomainDetail{ID: 1, Name: "a.test", OrgID: 5}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPUT(t, ts, "/admin/ldap/bindings/1", session, csrf, `{}`),
		http.StatusNoContent, "own-org binding edit")
	d.domainDetail.OrgID = 9
	wantStatus(t, authedPUT(t, ts, "/admin/ldap/bindings/1", session, csrf, `{}`),
		http.StatusForbidden, "other-org binding edit")
}

// TestLDAPConnectionsAreSystemOnly proves a domain admin can neither read nor
// change a connection, since its service account reaches beyond the domain.
func TestLDAPConnectionsAreSystemOnly(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedGET(t, ts, "/admin/ldap/connections", session), http.StatusForbidden, "connection list")
	wantStatus(t, authedGET(t, ts, "/admin/ldap/connections/1", session), http.StatusForbidden, "connection read")
	wantStatus(t, authedPOST(t, ts, "/admin/ldap/connections", session, csrf, `{"Name":"x","URI":"ldaps://x"}`),
		http.StatusForbidden, "connection create")
	wantStatus(t, authedPOST(t, ts, "/admin/ldap/bindings", session, csrf, `{"ConnectionID":1,"DomainID":1}`),
		http.StatusForbidden, "binding create")
}

// TestLDAPReadOnlySystemAdminReadsOnly proves a read-only system admin reads
// connections and bindings and is refused every change.
func TestLDAPReadOnlySystemAdminReadsOnly(t *testing.T) {
	d := ldapAPIDir()
	d.perms = []directory.Permission{{Name: directory.PermSystemAdminRO}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedGET(t, ts, "/admin/ldap/connections/1", session), http.StatusOK, "connection read")
	wantStatus(t, authedGET(t, ts, "/admin/ldap/bindings/2", session), http.StatusOK, "binding read")
	wantStatus(t, authedPUT(t, ts, "/admin/ldap/bindings/2", session, csrf, `{}`), http.StatusForbidden, "binding edit")
	wantStatus(t, authedPOST(t, ts, "/admin/ldap/bindings/2/reset", session, csrf, ``), http.StatusForbidden, "reset")
	wantStatus(t, authedReq(t, ts, "DELETE", "/admin/ldap/connections/1", session, csrf, ``),
		http.StatusForbidden, "connection delete")
}

// TestLDAPNonSystemEditKeepsSystemFields proves a domain admin's edit cannot change
// the search base, the filters or the connection: those decide what the service
// account reads, so they keep their stored values while the mapping changes.
func TestLDAPNonSystemEditKeepsSystemFields(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPUT(t, ts, "/admin/ldap/bindings/1", session, csrf,
		`{"ConnectionID":2,"Mapping":{"baseDN":"dc=everything","groupFilter":"(objectClass=*)","aliasAttr":"proxyAddresses"}}`),
		http.StatusNoContent, "domain admin edit")
	got := d.ldapBindings[1]
	wantEq(t, got.Mapping.BaseDN, "ou=a,dc=test", "the base DN keeps its stored value")
	wantEq(t, got.Mapping.GroupFilter, "(cn=a*)", "the group filter keeps its stored value")
	wantEq(t, got.ConnectionID, int64(1), "the connection keeps its stored value")
	wantEq(t, got.Mapping.AliasAttr, "proxyAddresses", "the alias attribute changes")
}

// TestLDAPSystemEditChangesSystemFields is the positive control: a system admin's
// edit reaches the base DN, the filters and the connection.
func TestLDAPSystemEditChangesSystemFields(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminSystem})
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPUT(t, ts, "/admin/ldap/bindings/1", session, csrf,
		`{"ConnectionID":2,"Mapping":{"baseDN":"ou=new,dc=test"}}`), http.StatusNoContent, "system edit")
	got := d.ldapBindings[1]
	wantEq(t, got.Mapping.BaseDN, "ou=new,dc=test", "the base DN changes")
	wantEq(t, got.ConnectionID, int64(2), "the connection changes")
}

// TestLDAPBindingRefusesMalformedAttribute proves an attribute name carrying filter
// syntax is refused before it reaches the directory.
func TestLDAPBindingRefusesMalformedAttribute(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	for _, body := range []string{
		`{"Mapping":{"aliasAttr":"mail)(uid=*"}}`,
		`{"Mapping":{"fields":{"title":{"attr":"title x","enabled":true}}}}`,
		`{"Mapping":{"fields":{"nonesuch":{"enabled":true}}}}`,
	} {
		wantStatus(t, authedPUT(t, ts, "/admin/ldap/bindings/1", session, csrf, body), http.StatusBadRequest, body)
	}
	wantEq(t, d.ldapBindings[1].Mapping.AliasAttr, "mail", "a refused edit changed nothing")
}

// TestValidAttrName pins the attribute-name grammar: descriptors and numeric OIDs
// pass, filter syntax and options do not.
func TestValidAttrName(t *testing.T) {
	for name, want := range map[string]bool{
		"mail": true, "proxyAddresses": true, "msDS-Foo": true, "2.5.4.3": true,
		"": false, "1mail": false, "mail)(uid=*": false, "a b": false, "2.5.": false, "userCertificate;binary": false,
	} {
		wantEq(t, validAttrName(name), want, "validAttrName("+name+")")
	}
}

// TestLDAPConnectionPasswordNeverDisclosed proves a read reports only that a
// password is stored, and an update with an empty password keeps it.
func TestLDAPConnectionPasswordNeverDisclosed(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminSystem})
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, authedGET(t, ts, "/admin/ldap/connections/1", session), http.StatusOK, "connection read")
	wantNotContains(t, body, "topsecret", "the bind password stays out of the read")
	wantContains(t, body, `"BindPasswordSet":true`, "the read reports a stored password")
	wantStatus(t, authedPUT(t, ts, "/admin/ldap/connections/1", session, csrf,
		`{"Name":"hq","URI":"ldaps://dc2.test","BindDN":"cn=svc"}`), http.StatusNoContent, "update without password")
	wantEq(t, d.ldapConns[1].BindPassword, "topsecret", "an empty password keeps the stored one")
	wantEq(t, d.ldapConns[1].URI, "ldaps://dc2.test", "the URI changes")
}

// TestLDAPBindingResetRestoresPreset proves a reset brings back the preset's mapping
// and leaves the system-only settings as they were.
func TestLDAPBindingResetRestoresPreset(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPOST(t, ts, "/admin/ldap/bindings/1/reset", session, csrf, ``), http.StatusNoContent, "reset")
	got := d.ldapBindings[1].Mapping
	wantEq(t, got.AliasAttr, "proxyAddresses", "the preset alias attribute is back")
	wantEq(t, got.Fields["displayName"].Enabled, true, "the preset fields are back")
	wantEq(t, got.BaseDN, "ou=a,dc=test", "the base DN keeps its stored value")
	wantStatus(t, authedPOST(t, ts, "/admin/ldap/bindings/2/reset", session, csrf, ``),
		http.StatusForbidden, "reset of another domain")
}

// TestLDAPBindingSyncQueuesTask proves the sync endpoint queues one task naming the
// binding, and reports sync as unavailable when no syncer is wired.
func TestLDAPBindingSyncQueuesTask(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServerWithSyncer(t, d, &fakeSyncer{})
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPOST(t, ts, "/admin/ldap/bindings/1/sync", session, csrf, ``), http.StatusAccepted, "sync")
	if len(d.tasks) != 1 || d.tasks[0].Type != "ldapsync" || d.tasks[0].Params != "1" {
		t.Errorf("queued tasks = %+v, want one ldapsync task for binding 1", d.tasks)
	}

	bare := adminServer(t, ldapAPIDir(directory.AdminRole{Role: directory.AdminSystem}))
	s2, c2 := loginCookies(t, bare)
	wantStatus(t, authedPOST(t, bare, "/admin/ldap/bindings/1/sync", s2, c2, ``),
		http.StatusServiceUnavailable, "sync without a syncer")
}

// TestLDAPBindingCreateFromPreset proves a system admin binds a domain from a preset
// and a second binding of the same domain is refused.
func TestLDAPBindingCreateFromPreset(t *testing.T) {
	d := ldapAPIDir(directory.AdminRole{Role: directory.AdminSystem})
	d.ldapBindings = nil
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPOST(t, ts, "/admin/ldap/bindings", session, csrf,
		`{"ConnectionID":1,"DomainID":3,"Preset":"inetorgperson"}`), http.StatusCreated, "bind from preset")
	wantEq(t, d.ldapBindings[1].Mapping.Fields["company"].Attr, "o", "the preset mapping is stored")
	wantStatus(t, authedPOST(t, ts, "/admin/ldap/bindings", session, csrf, `{"ConnectionID":2,"DomainID":3}`),
		http.StatusConflict, "second binding of the domain")
}
