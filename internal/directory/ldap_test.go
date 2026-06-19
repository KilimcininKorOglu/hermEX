package directory

import "testing"

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
	}
	if err := d.SetLDAPConfig(7, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.GetLDAPConfig(7)
	if err != nil || !ok {
		t.Fatalf("GetLDAPConfig after set: ok %v, err %v", ok, err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	// A second Set for the same org replaces the row rather than failing on the
	// primary key or leaving the old values.
	want.URI = "ldap://ad2.hermex.test:389"
	want.StartTLS = false
	if err := d.SetLDAPConfig(7, want); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := d.GetLDAPConfig(7); got != want {
		t.Errorf("replace mismatch:\n got %+v\nwant %+v", got, want)
	}
}
