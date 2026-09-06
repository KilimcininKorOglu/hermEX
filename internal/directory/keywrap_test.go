package directory

import (
	"strings"
	"testing"
)

const wrapTestPEM = "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAK\n-----END RSA PRIVATE KEY-----\n"

// TestWrapRoundTrip proves a wrapped key comes back exactly, and that the stored
// form carries none of it.
func TestWrapRoundTrip(t *testing.T) {
	d := &SQLDirectory{}
	d.SetKeySecret([]byte("a-long-operator-secret"))

	sealed, err := d.wrapKey(wrapDKIM, wrapTestPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, wrapPrefix) {
		t.Errorf("stored form = %q, want the wrap prefix", sealed)
	}
	if strings.Contains(sealed, "BEGIN") {
		t.Errorf("the stored form still shows the key:\n%s", sealed)
	}
	got, wrapped, err := d.unwrapKey(wrapDKIM, sealed)
	if err != nil || !wrapped {
		t.Fatalf("unwrap = (%v, %v)", wrapped, err)
	}
	if got != wrapTestPEM {
		t.Errorf("unwrapped key = %q, want the original", got)
	}
}

// TestWrapContextsAreSeparate proves a value cannot be moved from one key column
// to the other: the column is bound into the ciphertext.
func TestWrapContextsAreSeparate(t *testing.T) {
	d := &SQLDirectory{}
	d.SetKeySecret([]byte("a-long-operator-secret"))

	sealed, err := d.wrapKey(wrapDKIM, wrapTestPEM)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.unwrapKey(wrapTLS, sealed); err == nil {
		t.Error("a DKIM key opened as a TLS key")
	}
}

// TestWrapWrongSecretFails proves a mismatched secret is an error, not a
// key-shaped string that is not a key.
func TestWrapWrongSecretFails(t *testing.T) {
	d := &SQLDirectory{}
	d.SetKeySecret([]byte("the-original-secret"))
	sealed, err := d.wrapKey(wrapDKIM, wrapTestPEM)
	if err != nil {
		t.Fatal(err)
	}

	other := &SQLDirectory{}
	other.SetKeySecret([]byte("a-different-secret"))
	got, _, err := other.unwrapKey(wrapDKIM, sealed)
	if err == nil {
		t.Fatalf("unwrap with the wrong secret succeeded and returned %q", got)
	}
	if got != "" {
		t.Errorf("a failed unwrap returned %q, want nothing", got)
	}
}

// TestPlaintextRowIsReadable proves adopting encryption needs no migration: a row
// written before a secret existed still opens, and is reported as unwrapped so
// the caller rewrites it.
func TestPlaintextRowIsReadable(t *testing.T) {
	d := &SQLDirectory{}
	d.SetKeySecret([]byte("a-long-operator-secret"))

	got, wrapped, err := d.unwrapKey(wrapDKIM, wrapTestPEM)
	if err != nil {
		t.Fatal(err)
	}
	if wrapped {
		t.Error("a plaintext row was reported as wrapped")
	}
	if got != wrapTestPEM {
		t.Errorf("plaintext row = %q, want it unchanged", got)
	}
	if !d.rewrapNeeded(wrapped) {
		t.Error("a plaintext row with a secret configured must be rewritten")
	}
}

// TestWithoutSecretNothingChanges proves the unconfigured deployment behaves
// exactly as it did before.
func TestWithoutSecretNothingChanges(t *testing.T) {
	d := &SQLDirectory{}
	d.SetKeySecret(nil)

	sealed, err := d.wrapKey(wrapDKIM, wrapTestPEM)
	if err != nil {
		t.Fatal(err)
	}
	if sealed != wrapTestPEM {
		t.Errorf("stored form = %q, want the key unchanged", sealed)
	}
	if d.rewrapNeeded(false) {
		t.Error("a rewrite was requested with no secret configured")
	}
}

// TestWrappedRowNeedsTheSecret proves a daemon started without the secret reports
// an error rather than handing a caller the ciphertext as if it were a key.
func TestWrappedRowNeedsTheSecret(t *testing.T) {
	d := &SQLDirectory{}
	d.SetKeySecret([]byte("a-long-operator-secret"))
	sealed, err := d.wrapKey(wrapDKIM, wrapTestPEM)
	if err != nil {
		t.Fatal(err)
	}

	d.SetKeySecret(nil)
	if got, _, err := d.unwrapKey(wrapDKIM, sealed); err == nil {
		t.Errorf("unwrap without a secret returned %q, want an error", got)
	}
}

// storedDKIM reads a domain's key column exactly as the database holds it.
func storedDKIM(t *testing.T, d *SQLDirectory, domain string) string {
	t.Helper()
	var pk string
	if err := d.db.QueryRow(`SELECT private_key FROM dkim_keys WHERE domain = ?`, domain).Scan(&pk); err != nil {
		t.Fatal(err)
	}
	return pk
}

// storedTLSKey reads a certificate's key column exactly as the database holds it.
func storedTLSKey(t *testing.T, d *SQLDirectory, name string) string {
	t.Helper()
	var key string
	if err := d.db.QueryRow(`SELECT key_pem FROM tls_certs WHERE name = ?`, name).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

// TestDKIMKeyIsEncryptedAtRest proves the database no longer holds a usable
// signing key: a dump of it is not enough to sign for the domain.
func TestDKIMKeyIsEncryptedAtRest(t *testing.T) {
	d, _ := freshDirectory(t)
	d.SetKeySecret([]byte("a-long-operator-secret"))

	mustNoErr(t, "store the DKIM key",
		d.SetDKIMKey("hermex.test", "sel1", []byte(wrapTestPEM), "v=DKIM1; p=AAAA"))
	if at := storedDKIM(t, d, "hermex.test"); strings.Contains(at, "BEGIN") {
		t.Errorf("the key column still holds the key:\n%s", at)
	}
	// The signer and the operator's export both get the real key back.
	mustNoErr(t, "enable DKIM", d.SetDKIMEnabled("hermex.test", true))
	wantUnwrappedKey(t, "DKIMKey", func() ([]byte, bool, error) {
		pem, _, found, err := d.DKIMKey("hermex.test")
		return pem, found, err
	})
	wantUnwrappedKey(t, "ExportDKIMKey", func() ([]byte, bool, error) {
		pem, _, found, err := d.ExportDKIMKey("hermex.test")
		return pem, found, err
	})
}

// wantUnwrappedKey checks a key reader returns the original PEM, found.
func wantUnwrappedKey(t *testing.T, what string, read func() ([]byte, bool, error)) {
	t.Helper()
	pem, found, err := read()
	mustNoErr(t, what, err)
	wantEq(t, what+" found the key", found, true)
	wantEq(t, what+" returned the original key", string(pem), wrapTestPEM)
}

// TestDKIMPlaintextRowIsRewrapped proves an existing deployment converges on
// encryption with nothing to run: the first read of a plaintext key rewrites it.
func TestDKIMPlaintextRowIsRewrapped(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	// Written the way it was before the secret existed.
	if err := d.SetDKIMKey("hermex.test", "sel1", []byte(wrapTestPEM), "v=DKIM1; p=AAAA"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetDKIMEnabled("hermex.test", true); err != nil {
		t.Fatal(err)
	}
	if at := storedDKIM(t, d, "hermex.test"); !strings.Contains(at, "BEGIN") {
		t.Fatalf("the row was expected to start as plaintext, got %q", at)
	}

	d.SetKeySecret([]byte("a-long-operator-secret"))
	pem, _, found, err := d.DKIMKey("hermex.test")
	if err != nil || !found || string(pem) != wrapTestPEM {
		t.Fatalf("DKIMKey = (%q, %v, %v), want the original key", pem, found, err)
	}
	if at := storedDKIM(t, d, "hermex.test"); strings.Contains(at, "BEGIN") {
		t.Errorf("the plaintext row was not rewritten:\n%s", at)
	}
}

// TestTLSKeyIsEncryptedAtRest proves the same for an uploaded serving key.
func TestTLSKeyIsEncryptedAtRest(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	d.SetKeySecret([]byte("a-long-operator-secret"))

	if err := d.SetTLSCert("mail.hermex.test", "cert-pem", wrapTestPEM, 0); err != nil {
		t.Fatal(err)
	}
	if at := storedTLSKey(t, d, "mail.hermex.test"); strings.Contains(at, "BEGIN") {
		t.Errorf("the key column still holds the key:\n%s", at)
	}
	certs, err := d.LoadTLSCerts()
	if err != nil || len(certs) != 1 {
		t.Fatalf("LoadTLSCerts = (%v, %v)", certs, err)
	}
	if certs[0].KeyPEM != wrapTestPEM {
		t.Errorf("loaded key = %q, want the original", certs[0].KeyPEM)
	}
}

// TestTLSPlaintextRowIsRewrapped is the rewrite property for the TLS column.
func TestTLSPlaintextRowIsRewrapped(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	if err := d.SetTLSCert("mail.hermex.test", "cert-pem", wrapTestPEM, 0); err != nil {
		t.Fatal(err)
	}
	d.SetKeySecret([]byte("a-long-operator-secret"))
	if _, err := d.LoadTLSCerts(); err != nil {
		t.Fatal(err)
	}
	if at := storedTLSKey(t, d, "mail.hermex.test"); strings.Contains(at, "BEGIN") {
		t.Errorf("the plaintext row was not rewritten:\n%s", at)
	}
}
