package directory

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// storedBindPassword reads a connection's bind_password column as stored.
func storedBindPassword(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var s string
	mustNoErr(t, "read the stored bind password",
		db.QueryRow(`SELECT bind_password FROM ldap_connections WHERE id = ?`, id).Scan(&s))
	return s
}

// rerunLDAPConnectionsMigration marks 0061 and every later migration not applied
// and applies them again, as an upgrade from the per-organization table does.
func rerunLDAPConnectionsMigration(t *testing.T, d *SQLDirectory, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`DELETE FROM schema_migrations WHERE version >= 61`)
	mustNoErr(t, "mark the migration as not applied", err)
	mustNoErr(t, "apply the migration", d.EnsureSchema())
}

// TestLDAPConnectionsMigrationCopiesOrgConfig proves the upgrade turns an
// organization's directory into one connection with a binding per domain, keeps
// contact sync only on the binding of the old contact domain, skips a directory
// that was switched off, and inserts nothing more when it runs a second time.
func TestLDAPConnectionsMigrationCopiesOrgConfig(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	aID := mustCreateDomain(t, d, root, "a.test")
	bID := mustCreateDomain(t, d, root, "b.test")
	_, err := db.Exec(
		`INSERT INTO ldap_config (org_id, uri, start_tls, bind_dn, bind_password, base_dn, username_attr, sync_config)
		 VALUES (0, 'ldaps://ad.test', 0, 'cn=svc', 'pw', 'dc=test', 'mail', ?),
		        (5, '', 0, '', '', '', 'mail', NULL)`,
		`{"aliasAttr":"proxyAddresses","syncContacts":true,"contactDomain":"B.test"}`)
	mustNoErr(t, "seed the organization directories", err)

	rerunLDAPConnectionsMigration(t, d, db)
	rerunLDAPConnectionsMigration(t, d, db)

	wantRows(t, db, "connections", 1, `SELECT COUNT(*) FROM ldap_connections`)
	wantRows(t, db, "bindings", 2, `SELECT COUNT(*) FROM ldap_bindings`)
	a, ok, err := d.LDAPConfigForDomain(aID)
	mustNoErr(t, "resolve a.test", err)
	wantEq(t, "a.test is bound", ok, true)
	wantEq(t, "a.test URI", a.URI, "ldaps://ad.test")
	wantEq(t, "a.test bind password", a.BindPassword, "pw")
	wantEq(t, "a.test domain", a.Domain, "a.test")
	wantEq(t, "a.test alias attribute", a.AliasAttr, "proxyAddresses")
	wantEq(t, "a.test contact sync", a.SyncContacts, false)
	b, _, err := d.LDAPConfigForDomain(bID)
	mustNoErr(t, "resolve b.test", err)
	wantEq(t, "b.test contact sync", b.SyncContacts, true)
}

// TestLDAPConnectionCRUD proves a connection round-trips with its bind password
// wrapped at rest, updates in place, and takes its bindings with it on delete.
func TestLDAPConnectionCRUD(t *testing.T) {
	d, db := freshDirectory(t)
	d.SetKeySecret([]byte("test-key-secret"))
	domID := mustCreateDomain(t, d, t.TempDir(), "crud.test")
	want := LDAPConnection{Name: "hq", URI: "ldap://ad.test", StartTLS: true, BindDN: "cn=svc",
		BindPassword: "s3cret", BaseDN: "dc=test", UsernameAttr: "mail"}
	id, err := d.CreateLDAPConnection(want)
	mustNoErr(t, "create the connection", err)
	want.ID = id
	if s := storedBindPassword(t, db, id); !strings.HasPrefix(s, wrapPrefix) {
		t.Errorf("the bind password is stored unwrapped: %q", s)
	}
	got, ok, err := d.GetLDAPConnection(id)
	mustNoErr(t, "read the connection", err)
	wantEq(t, "the connection exists", ok, true)
	wantEq(t, "the connection", got, want)

	_, err = d.CreateLDAPConnection(want)
	wantEq(t, "a duplicate name is refused", errors.Is(err, ErrLDAPConnectionName), true)

	want.BaseDN = "ou=people,dc=test"
	found, err := d.UpdateLDAPConnection(want)
	mustNoErr(t, "update the connection", err)
	wantEq(t, "the update found the connection", found, true)
	got, _, err = d.GetLDAPConnection(id)
	mustNoErr(t, "read the updated connection", err)
	wantEq(t, "the updated base DN", got.BaseDN, want.BaseDN)
	list, err := d.ListLDAPConnections()
	mustNoErr(t, "list the connections", err)
	wantEq(t, "listed connections", len(list), 1)
	wantEq(t, "a listing carries no password", list[0].BindPassword, "")

	_, err = d.CreateLDAPBinding(LDAPBinding{ConnectionID: id, DomainID: domID, Preset: LDAPPresetAD})
	mustNoErr(t, "bind the domain", err)
	deleted, err := d.DeleteLDAPConnection(id)
	mustNoErr(t, "delete the connection", err)
	wantEq(t, "the connection was deleted", deleted, true)
	wantRows(t, db, "bindings left", 0, `SELECT COUNT(*) FROM ldap_bindings`)
}

// TestLDAPConnectionRefusesPlaintext proves a connection that would bind in the
// clear is refused, on create and on update.
func TestLDAPConnectionRefusesPlaintext(t *testing.T) {
	d, _ := freshDirectory(t)
	c := LDAPConnection{Name: "clear", URI: "ldap://ad.test"}
	_, err := d.CreateLDAPConnection(c)
	wantEq(t, "a plaintext create is refused", errors.Is(err, ErrInsecureLDAP), true)
	c.StartTLS = true
	id, err := d.CreateLDAPConnection(c)
	mustNoErr(t, "create with StartTLS", err)
	c.ID, c.StartTLS = id, false
	_, err = d.UpdateLDAPConnection(c)
	wantEq(t, "a plaintext update is refused", errors.Is(err, ErrInsecureLDAP), true)
}

// TestLDAPDomainBindsOnce proves a domain binds to one connection only, an unknown
// preset is refused, and a binding's mapping and base DN override round-trip.
func TestLDAPDomainBindsOnce(t *testing.T) {
	d, _ := freshDirectory(t)
	domID := mustCreateDomain(t, d, t.TempDir(), "once.test")
	c1, err := d.CreateLDAPConnection(LDAPConnection{Name: "one", URI: "ldaps://one.test"})
	mustNoErr(t, "create the first connection", err)
	c2, err := d.CreateLDAPConnection(LDAPConnection{Name: "two", URI: "ldaps://two.test"})
	mustNoErr(t, "create the second connection", err)

	_, err = d.CreateLDAPBinding(LDAPBinding{ConnectionID: c1, DomainID: domID, Preset: "nonesuch"})
	wantEq(t, "an unknown preset is refused", errors.Is(err, ErrLDAPPreset), true)
	mapping := LDAPMapping{BaseDN: "ou=once,dc=test", AliasAttr: "proxyAddresses"}
	bID, err := d.CreateLDAPBinding(LDAPBinding{ConnectionID: c1, DomainID: domID, Mapping: mapping})
	mustNoErr(t, "bind the domain", err)
	_, err = d.CreateLDAPBinding(LDAPBinding{ConnectionID: c2, DomainID: domID})
	wantEq(t, "a second binding is refused", errors.Is(err, ErrLDAPDomainBound), true)

	cfg, ok, err := d.LDAPConfigForBinding(bID)
	mustNoErr(t, "resolve the binding", err)
	wantEq(t, "the binding resolves", ok, true)
	wantEq(t, "the effective URI", cfg.URI, "ldaps://one.test")
	wantEq(t, "the base DN override", cfg.BaseDN, "ou=once,dc=test")
	wantEq(t, "the bound domain id", cfg.DomainID, domID)
	wantEq(t, "the bound domain", cfg.Domain, "once.test")

	found, err := d.UpdateLDAPBinding(LDAPBinding{ID: bID, ConnectionID: c2, Mapping: mapping})
	mustNoErr(t, "move the binding", err)
	wantEq(t, "the update found the binding", found, true)
	cfg, _, err = d.LDAPConfigForDomain(domID)
	mustNoErr(t, "resolve the moved binding", err)
	wantEq(t, "the moved binding's URI", cfg.URI, "ldaps://two.test")
}

// TestPurgeDomainRemovesLDAPBinding proves purging a domain drops its binding.
func TestPurgeDomainRemovesLDAPBinding(t *testing.T) {
	d, db := freshDirectory(t)
	domID := mustCreateDomain(t, d, t.TempDir(), "purge.test")
	cID, err := d.CreateLDAPConnection(LDAPConnection{Name: "p", URI: "ldaps://p.test"})
	mustNoErr(t, "create the connection", err)
	_, err = d.CreateLDAPBinding(LDAPBinding{ConnectionID: cID, DomainID: domID})
	mustNoErr(t, "bind the domain", err)
	_, err = d.PurgeDomain(domID, false)
	mustNoErr(t, "purge the domain", err)
	wantRows(t, db, "bindings left", 0, `SELECT COUNT(*) FROM ldap_bindings`)
	wantRows(t, db, "connections left", 1, `SELECT COUNT(*) FROM ldap_connections`)
}

// TestLDAPBindPasswordWrappedAtStartup proves EnsureSchema wraps a plaintext bind
// password once a key secret is installed, and the wrapped row still reads back.
func TestLDAPBindPasswordWrappedAtStartup(t *testing.T) {
	d, db := freshDirectory(t)
	res, err := db.Exec(`INSERT INTO ldap_connections (name, uri, bind_password) VALUES ('legacy', 'ldaps://l.test', 'plain')`)
	mustNoErr(t, "seed a plaintext row", err)
	id, err := res.LastInsertId()
	mustNoErr(t, "read the row id", err)

	d.SetKeySecret([]byte("test-key-secret"))
	mustNoErr(t, "ensure schema", d.EnsureSchema())
	if s := storedBindPassword(t, db, id); !strings.HasPrefix(s, wrapPrefix) {
		t.Errorf("the bind password is still plaintext after startup: %q", s)
	}
	c, _, err := d.GetLDAPConnection(id)
	mustNoErr(t, "read the connection", err)
	wantEq(t, "the unwrapped bind password", c.BindPassword, "plain")
}

// TestLDAPPresets proves every listed preset resolves and an unknown one does not.
func TestLDAPPresets(t *testing.T) {
	for _, name := range LDAPPresetNames() {
		m, ok := LDAPPreset(name)
		wantEq(t, "preset "+name+" resolves", ok, true)
		wantEq(t, "preset "+name+" enables display name", m.Fields["displayName"].Enabled, true)
	}
	_, ok := LDAPPreset("nonesuch")
	wantEq(t, "an unknown preset resolves", ok, false)
}
