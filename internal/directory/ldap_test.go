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
// LDAP-mastered account against the verifier (and is denied, never falling back
// to the local hash, when no verifier is installed).
func TestAuthenticateLDAPBranch(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "local@hermex.test", "localpass")
	mustCreateUser(t, d, root, "ext@hermex.test", "ignored-local-hash")
	// Master ext@ in LDAP (externid set) and give its org a directory config.
	_, err := db.Exec(`UPDATE users SET externid=? WHERE username=?`, []byte{0x01, 0x02}, "ext@hermex.test")
	mustNoErr(t, "master the account in LDAP", err)
	mustNoErr(t, "set the LDAP config", d.SetLDAPConfig(0,
		LDAPConfig{URI: "ldaps://ad.hermex.test", BaseDN: "dc=hermex,dc=test", UsernameAttr: "mail"}))
	admits := func(login, password string) bool {
		t.Helper()
		_, ok := d.Authenticate(login, password)
		return ok
	}

	// 1. A local account still authenticates against its crypt hash.
	wantEq(t, "local crypt authentication", admits("local@hermex.test", "localpass"), true)

	// 2. An LDAP-mastered account is denied with no verifier, and must NOT be
	// admitted by its (irrelevant) local hash.
	wantEq(t, "an LDAP-mastered login with no verifier installed", admits("ext@hermex.test", "anything"), false)
	wantEq(t, "an LDAP-mastered login falling back to the local crypt hash",
		admits("ext@hermex.test", "ignored-local-hash"), false)

	// 3. With an accepting verifier it authenticates, and the verifier receives
	// the resolved config plus the login and password.
	stub := &stubVerifier{result: true}
	d.SetLDAPVerifier(stub)
	wantEq(t, "an LDAP login with an accepting verifier", admits("ext@hermex.test", "ldappass"), true)
	wantEq(t, "the login the verifier saw", stub.gotLogin, "ext@hermex.test")
	wantEq(t, "the password the verifier saw", stub.gotPass, "ldappass")
	wantEq(t, "the config the verifier saw", stub.gotCfg.URI, "ldaps://ad.hermex.test")

	// 4. A rejecting verifier denies the login.
	d.SetLDAPVerifier(&stubVerifier{result: false})
	wantEq(t, "an LDAP login the verifier rejected", admits("ext@hermex.test", "wrong"), false)
}

// TestUpsertLDAPUser proves a downsync marks an existing user LDAP-mastered (sets
// its externid) and creates a brand-new user carrying its externid.
func TestUpsertLDAPUser(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "localpw")

	// An existing user gains its externid (created=false).
	created, err := d.UpsertLDAPUser("alice@hermex.test", []byte{0x01, 0x02}, "")
	mustNoErr(t, "upsert an existing user", err)
	wantEq(t, "the upsert created the existing user", created, false)
	wantMastered(t, d, "alice@hermex.test")

	// A new login is created with its externid (created=true).
	created, err = d.UpsertLDAPUser("bob@hermex.test", []byte{0x03, 0x04}, filepath.Join(root, "bob"))
	mustNoErr(t, "upsert a new user", err)
	wantEq(t, "the upsert created the new user", created, true)
	wantMastered(t, d, "bob@hermex.test")
}

// wantMastered checks a login exists and carries an externid, which is what marks
// it LDAP-mastered.
func wantMastered(t *testing.T, d *SQLDirectory, login string) {
	t.Helper()
	row, ok, err := d.resolve(login)
	mustNoErr(t, "resolve "+login, err)
	wantEq(t, login+" exists", ok, true)
	if len(row.externid) == 0 {
		t.Errorf("%s carries no externid, so it is not LDAP-mastered", login)
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
	// primary key or leaving the old values. The replacement stays encrypted:
	// SetLDAPConfig refuses a plaintext bind whichever write it is.
	want.URI = "ldaps://ad2.hermex.test:636"
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
