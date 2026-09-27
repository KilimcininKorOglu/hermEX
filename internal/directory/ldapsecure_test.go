package directory

import (
	"testing"
)

// TestEncryptedTransport covers the combinations an operator can produce in the
// connection form. Scheme and StartTLS are independent fields, and only their
// combination decides whether a bind is in the clear. The storage side of the rule
// is TestLDAPConnectionRefusesPlaintext.
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
