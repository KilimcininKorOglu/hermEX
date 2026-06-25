package directory

import (
	"path/filepath"
	"reflect"
	"testing"
)

// stubVerifier is a test LDAPVerifier that records the inputs it was called with
// and returns a fixed verdict.
type stubVerifier struct {
	result   bool
	err      error
	gotCfg   LDAPConfig
	gotLogin string
	gotPass  string
}

func (s *stubVerifier) Verify(cfg LDAPConfig, login, password string) (bool, error) {
	s.gotCfg, s.gotLogin, s.gotPass = cfg, login, password
	return s.result, s.err
}

// TestAuthenticateLDAPBranch proves the auth chain queries MySQL first, then
// routes by externid: a local account verifies against its crypt hash, an
// LDAP-mastered account against the verifier (and is denied — never falling back
// to the local hash — when no verifier is installed).
func TestAuthenticateLDAPBranch(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "domains", "hermex.test")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("local@hermex.test", "localpass", filepath.Join(root, "users", "local")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("ext@hermex.test", "ignored-local-hash", filepath.Join(root, "users", "ext")); err != nil {
		t.Fatal(err)
	}
	// Master ext@ in LDAP (externid set) and give its org a directory config.
	if _, err := db.Exec(`UPDATE users SET externid=? WHERE username=?`, []byte{0x01, 0x02}, "ext@hermex.test"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetLDAPConfig(0, LDAPConfig{URI: "ldap://ad.hermex.test", BaseDN: "dc=hermex,dc=test", UsernameAttr: "mail"}); err != nil {
		t.Fatal(err)
	}

	// 1. A local account still authenticates against its crypt hash.
	if _, ok := d.Authenticate("local@hermex.test", "localpass"); !ok {
		t.Error("local crypt authentication failed")
	}

	// 2. An LDAP-mastered account is denied with no verifier — and must NOT be
	// admitted by its (irrelevant) local hash.
	if _, ok := d.Authenticate("ext@hermex.test", "anything"); ok {
		t.Error("LDAP-mastered login succeeded with no verifier installed")
	}
	if _, ok := d.Authenticate("ext@hermex.test", "ignored-local-hash"); ok {
		t.Error("LDAP-mastered login fell back to the local crypt hash")
	}

	// 3. With an accepting verifier it authenticates, and the verifier receives
	// the resolved config plus the login and password.
	stub := &stubVerifier{result: true}
	d.SetLDAPVerifier(stub)
	if _, ok := d.Authenticate("ext@hermex.test", "ldappass"); !ok {
		t.Fatal("LDAP authentication with an accepting verifier failed")
	}
	if stub.gotLogin != "ext@hermex.test" || stub.gotPass != "ldappass" || stub.gotCfg.URI != "ldap://ad.hermex.test" {
		t.Errorf("verifier saw cfg=%+v login=%q pass=%q; want the resolved config + login/password",
			stub.gotCfg, stub.gotLogin, stub.gotPass)
	}

	// 4. A rejecting verifier denies the login.
	d.SetLDAPVerifier(&stubVerifier{result: false})
	if _, ok := d.Authenticate("ext@hermex.test", "wrong"); ok {
		t.Error("LDAP authentication succeeded when the verifier rejected")
	}
}

// TestUpsertLDAPUser proves a downsync marks an existing user LDAP-mastered (sets
// its externid) and creates a brand-new user carrying its externid.
func TestUpsertLDAPUser(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "dom")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("alice@hermex.test", "localpw", filepath.Join(root, "alice")); err != nil {
		t.Fatal(err)
	}

	// An existing user gains its externid (created=false).
	if created, err := d.UpsertLDAPUser("alice@hermex.test", []byte{0x01, 0x02}, ""); err != nil || created {
		t.Fatalf("upsert existing = (created %v, err %v); want (false, nil)", created, err)
	}
	if row, ok, err := d.resolve("alice@hermex.test"); err != nil || !ok || len(row.externid) == 0 {
		t.Errorf("existing user not marked LDAP-mastered (externid=%v, ok=%v, err=%v)", row.externid, ok, err)
	}

	// A new login is created with its externid (created=true).
	if created, err := d.UpsertLDAPUser("bob@hermex.test", []byte{0x03, 0x04}, filepath.Join(root, "bob")); err != nil || !created {
		t.Fatalf("upsert new = (created %v, err %v); want (true, nil)", created, err)
	}
	if row, ok, err := d.resolve("bob@hermex.test"); err != nil || !ok || len(row.externid) == 0 {
		t.Errorf("new LDAP user not created with externid (ok=%v, err=%v)", ok, err)
	}
}

// TestLDAPConfigRoundTrip stores an organization's LDAP configuration and reads
// it back, confirms SetLDAPConfig replaces rather than duplicates, and confirms
// an org with no configuration reports ok=false (so its users fall back to local
// crypt authentication).
func TestLDAPConfigRoundTrip(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	if _, ok, err := d.GetLDAPConfig(7); err != nil || ok {
		t.Fatalf("GetLDAPConfig(unconfigured) = ok %v, err %v; want ok=false", ok, err)
	}

	want := LDAPConfig{
		URI:          "ldaps://ad.hermex.test:636",
		StartTLS:     true,
		BindDN:       "cn=svc,dc=hermex,dc=test",
		BindPassword: "s3cret",
		BaseDN:       "ou=people,dc=hermex,dc=test",
		UsernameAttr: "userPrincipalName",
		SyncFields: map[string]LDAPSyncField{
			"displayName": {Enabled: true},
			"title":       {Enabled: true, Attr: "jobTitle"},
		},
		SyncGroups:  true,
		GroupBaseDN: "ou=groups,dc=hermex,dc=test",
		GroupFilter: "(&(objectClass=group)(mail=*))",
	}
	if err := d.SetLDAPConfig(7, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.GetLDAPConfig(7)
	if err != nil || !ok {
		t.Fatalf("GetLDAPConfig after set: ok %v, err %v", ok, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	// A second Set for the same org replaces the row rather than failing on the
	// primary key or leaving the old values.
	want.URI = "ldap://ad2.hermex.test:389"
	want.StartTLS = false
	if err := d.SetLDAPConfig(7, want); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := d.GetLDAPConfig(7); !reflect.DeepEqual(got, want) {
		t.Errorf("replace mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// TestEnabledProfileSync resolves the enabled profile fields to their LDAP
// attributes: a disabled field is dropped, an enabled field with no override uses
// the standard attribute, and an explicit override wins.
func TestEnabledProfileSync(t *testing.T) {
	cfg := LDAPConfig{SyncFields: map[string]LDAPSyncField{
		"displayName": {Enabled: true},                   // standard attribute
		"title":       {Enabled: true, Attr: "jobTitle"}, // override
		"department":  {Enabled: false, Attr: "ou"},      // disabled, dropped
		"photo":       {Enabled: true},                   // standard thumbnailPhoto
	}}
	want := map[string]string{
		"displayName": "displayName",
		"title":       "jobTitle",
		"photo":       "thumbnailPhoto",
	}
	if got := cfg.EnabledProfileSync(); !reflect.DeepEqual(got, want) {
		t.Errorf("EnabledProfileSync = %v, want %v", got, want)
	}
}
