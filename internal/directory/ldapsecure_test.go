package directory

import (
	"errors"
	"testing"
)

// TestEncryptedTransport covers the combinations an operator can produce in the
// Directory Sync form. Scheme and StartTLS are independent fields, and only their
// combination decides whether a bind is in the clear.
func TestEncryptedTransport(t *testing.T) {
	secure := []LDAPConfig{
		{URI: "ldaps://dc.example.com:636"},
		{URI: "LDAPS://dc.example.com:636"},
		{URI: " ldaps://dc.example.com:636 "},
		{URI: "ldap://dc.example.com:389", StartTLS: true},
		{URI: "ldaps://dc.example.com:636", StartTLS: true},
	}
	for _, cfg := range secure {
		if !cfg.EncryptedTransport() {
			t.Errorf("EncryptedTransport(%+v) = false, want true", cfg)
		}
	}
	plain := []LDAPConfig{
		{URI: "ldap://dc.example.com:389"},
		{URI: "dc.example.com:389"},
		{URI: "ldap://dc.example.com"},
	}
	for _, cfg := range plain {
		if cfg.EncryptedTransport() {
			t.Errorf("EncryptedTransport(%+v) = true, want false", cfg)
		}
	}
}

// TestSetLDAPConfigRefusesPlaintext proves the configuration surface will not
// store a directory that binds in the clear, and still lets an operator turn the
// directory off by clearing the URI.
func TestSetLDAPConfigRefusesPlaintext(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	err := d.SetLDAPConfig(1, LDAPConfig{URI: "ldap://dc.example.com:389", BaseDN: "dc=example,dc=com"})
	if !errors.Is(err, ErrInsecureLDAP) {
		t.Fatalf("err = %v, want ErrInsecureLDAP", err)
	}
	if _, ok, err := d.GetLDAPConfig(1); ok || err != nil {
		t.Errorf("a refused configuration was stored anyway (ok=%v, err=%v)", ok, err)
	}

	// StartTLS on the same URI is accepted, and so is clearing the directory.
	if err := d.SetLDAPConfig(1, LDAPConfig{URI: "ldap://dc.example.com:389", StartTLS: true, BaseDN: "dc=example,dc=com"}); err != nil {
		t.Errorf("a StartTLS configuration was refused: %v", err)
	}
	if err := d.SetLDAPConfig(1, LDAPConfig{}); err != nil {
		t.Errorf("clearing the directory was refused: %v", err)
	}
}
