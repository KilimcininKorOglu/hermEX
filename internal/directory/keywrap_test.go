package directory

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
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
	u, err := d.unwrapKey(wrapDKIM, sealed)
	if err != nil || !u.wrapped {
		t.Fatalf("unwrap = (%v, %v)", u.wrapped, err)
	}
	if u.plain != wrapTestPEM {
		t.Errorf("unwrapped key = %q, want the original", u.plain)
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
	if u, err := d.unwrapKey(wrapTLS, sealed); err == nil || u.plain != "" || !u.wrapped {
		t.Errorf("a DKIM key opened as a TLS key: (%+v, %v)", u, err)
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
	u, err := other.unwrapKey(wrapDKIM, sealed)
	if err == nil {
		t.Fatalf("unwrap with the wrong secret succeeded and returned %q", u.plain)
	}
	if u.plain != "" {
		t.Errorf("a failed unwrap returned %q, want nothing", u.plain)
	}
}

// TestWrapRefusesATamperedValue changes one character of a stored value. Whoever
// can write the database must not be able to alter a key undetected, so the
// authenticated encryption has to refuse it and return nothing.
func TestWrapRefusesATamperedValue(t *testing.T) {
	d := &SQLDirectory{}
	d.SetKeySecret([]byte("a-long-operator-secret"))
	sealed, err := d.wrapKey(wrapDKIM, wrapTestPEM)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(sealed)
	i := len(wrapPrefix) + (len(body)-len(wrapPrefix))/2
	if body[i] == 'A' {
		body[i] = 'B'
	} else {
		body[i] = 'A'
	}
	if u, err := d.unwrapKey(wrapDKIM, string(body)); err == nil || u.plain != "" || !u.wrapped {
		t.Errorf("a tampered value unwrapped to %q (wrapped %v, err %v), want a refusal", u.plain, u.wrapped, err)
	}
}

// TestWrapUsesAFreshNonce wraps one key twice. AES-GCM loses both secrecy and
// integrity when a nonce repeats under one key, so the two stored forms must
// differ.
func TestWrapUsesAFreshNonce(t *testing.T) {
	d := &SQLDirectory{}
	d.SetKeySecret([]byte("a-long-operator-secret"))
	a, err := d.wrapKey(wrapDKIM, wrapTestPEM)
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.wrapKey(wrapDKIM, wrapTestPEM)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two wraps of one key are identical, so the nonce repeats")
	}
}

// TestPlaintextRowIsReadable proves adopting encryption needs no migration: a row
// written before a secret existed still opens, and is reported as unwrapped so
// the caller rewrites it.
func TestPlaintextRowIsReadable(t *testing.T) {
	d := &SQLDirectory{}
	d.SetKeySecret([]byte("a-long-operator-secret"))

	u, err := d.unwrapKey(wrapDKIM, wrapTestPEM)
	if err != nil {
		t.Fatal(err)
	}
	if u.wrapped {
		t.Error("a plaintext row was reported as wrapped")
	}
	if u.plain != wrapTestPEM {
		t.Errorf("plaintext row = %q, want it unchanged", u.plain)
	}
	if !d.rewrapNeeded(u.wrapped) {
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
	if u, err := d.unwrapKey(wrapDKIM, sealed); err == nil || u.plain != "" || !u.wrapped {
		t.Errorf("unwrap without a secret = (%+v, %v), want nothing, wrapped and an error", u, err)
	}
}

// TestWrapRefusesAMalformedValue proves a stored value carrying the wrap prefix
// but no openable body is an error with nothing returned: not base64, and shorter
// than the nonce. Read as plaintext, either would reach a signer as a "key".
func TestWrapRefusesAMalformedValue(t *testing.T) {
	d := &SQLDirectory{}
	d.SetKeySecret([]byte("a-long-operator-secret"))
	for _, stored := range []string{wrapPrefix + "!!not base64!!", wrapPrefix + "AAAA", wrapPrefix} {
		u, err := d.unwrapKey(wrapDKIM, stored)
		if err == nil || u.plain != "" || !u.wrapped {
			t.Errorf("unwrapKey(%q) = (%q, wrapped %v, %v), want nothing, wrapped and an error", stored, u.plain, u.wrapped, err)
		}
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
	d := freshSQL(t)
	d.SetKeySecret([]byte("a-long-operator-secret"))

	mustNoErr(t, "store the DKIM key",
		d.SetDKIMKey("hermex.test", "sel1", []byte(wrapTestPEM), "v=DKIM1; p=AAAA"))
	if at := storedDKIM(t, d, "hermex.test"); strings.Contains(at, "BEGIN") {
		t.Errorf("the key column still holds the key:\n%s", at)
	}
	// The signer and the operator's export both get the real key back.
	mustNoErr(t, "enable DKIM", d.SetDKIMEnabled("hermex.test", true))
	wantUnwrappedKey(t, "DKIMKey", func() ([]byte, bool, error) {
		pem, sel, found, err := d.DKIMKey("hermex.test")
		wantEq(t, "DKIMKey selector", sel, "sel1")
		return pem, found, err
	})
	wantUnwrappedKey(t, "ExportDKIMKey", func() ([]byte, bool, error) {
		pem, sel, found, err := d.ExportDKIMKey("hermex.test")
		wantEq(t, "ExportDKIMKey selector", sel, "sel1")
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
	pem, sel, found, err := d.DKIMKey("hermex.test")
	if err != nil || !found || string(pem) != wrapTestPEM || sel != "sel1" {
		t.Fatalf("DKIMKey = (%q, %q, %v, %v), want the original key", pem, sel, found, err)
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
	certs, err := d.LoadTLSCerts()
	mustNoErr(t, "LoadTLSCerts", err)
	wantEq(t, "loaded cert count", len(certs), 1)
	wantEq(t, "loaded key matches original", certs[0].KeyPEM, wrapTestPEM)
	if at := storedTLSKey(t, d, "mail.hermex.test"); strings.Contains(at, "BEGIN") {
		t.Errorf("the plaintext row was not rewritten:\n%s", at)
	}
}

// TestHMACSHA256MatchesCryptoHMAC proves the writer-free RFC 2104 derivation
// helper produces the exact digest of crypto/hmac for both short and >block-size keys.
func TestHMACSHA256MatchesCryptoHMAC(t *testing.T) {
	msg := []byte("hermex.directory.keywrap.v1")
	for _, secret := range [][]byte{
		[]byte("short-secret"),
		bytes.Repeat([]byte("k"), sha256.BlockSize),
		bytes.Repeat([]byte("x"), sha256.BlockSize+17),
	} {
		h := hmac.New(sha256.New, secret)
		if _, err := h.Write(msg); err != nil {
			t.Fatal(err)
		}
		want := h.Sum(nil)
		got := hmacSHA256(secret, msg)
		if !bytes.Equal(got, want) {
			t.Errorf("hmacSHA256(len=%d) = %x, want %x", len(secret), got, want)
		}
	}
}
