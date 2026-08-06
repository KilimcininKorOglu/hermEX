package webmail2api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"

	"hermex/internal/objectstore"
	"hermex/internal/smime"
)

// testIdentity mints a throwaway key and self-signed certificate, the shape a
// user's S/MIME identity has.
func testIdentity(t *testing.T) (*ecdsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "alice@hermex.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}

// TestSmimeAtRestPasswordIsPerMailbox proves two mailboxes do not share an
// at-rest password. With one deployment-wide password, a single container that
// leaked alongside the server secret opened every server-mode identity in the
// deployment; now each one is only as exposed as itself.
func TestSmimeAtRestPasswordIsPerMailbox(t *testing.T) {
	secret := []byte("smime-at-rest-test-secret")
	one, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()

	p1, err := smimeStorePassword(one, secret)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := smimeStorePassword(two, secret)
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Error("two mailboxes derived the same at-rest password; one leaked container would open both")
	}
	if p1 == legacySmimeP12Password(secret) || p2 == legacySmimeP12Password(secret) {
		t.Error("the per-mailbox password equals the deployment-wide one")
	}
	// Stable for a given mailbox, or the key would be unreadable on the next call.
	again, err := smimeStorePassword(one, secret)
	if err != nil {
		t.Fatal(err)
	}
	if again != p1 {
		t.Error("the same mailbox derived a different password on a second call")
	}
}

// TestSmimeIdentityUnlocksUnderThePerMailboxPassword proves a stored identity is
// readable back: the derivation is only useful if the server can still sign with
// the key it wrote.
func TestSmimeIdentityUnlocksUnderThePerMailboxPassword(t *testing.T) {
	secret := []byte("smime-at-rest-test-secret")
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	key, cert := testIdentity(t)
	password, err := smimeStorePassword(st, secret)
	if err != nil {
		t.Fatal(err)
	}
	p12, err := pkcs12.Modern.Encode(key, cert, nil, password)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSmimeIdentity(objectstore.SmimeIdentity{Mode: "server", Cert: cert.Raw, P12: p12}); err != nil {
		t.Fatal(err)
	}

	got, gotCert, ok := unlockSmimeIdentity(st, secret)
	if !ok {
		t.Fatal("the stored identity did not unlock")
	}
	if got == nil || gotCert == nil || gotCert.Subject.CommonName != "alice@hermex.test" {
		t.Error("the unlocked identity is not the one that was stored")
	}
}

// TestLegacySmimeIdentityMigratesOnFirstUse proves an identity written under the
// old deployment-wide password still opens, and is rewritten under the mailbox's
// own password on the way out. Without that, this change would lock every
// existing server-mode user out of their own key.
func TestLegacySmimeIdentityMigratesOnFirstUse(t *testing.T) {
	secret := []byte("smime-at-rest-test-secret")
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	key, cert := testIdentity(t)
	legacy, err := pkcs12.Modern.Encode(key, cert, nil, legacySmimeP12Password(secret))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSmimeIdentity(objectstore.SmimeIdentity{Mode: "server", Cert: cert.Raw, P12: legacy}); err != nil {
		t.Fatal(err)
	}

	if _, _, ok := unlockSmimeIdentity(st, secret); !ok {
		t.Fatal("an identity stored under the old password no longer unlocks")
	}
	// It must now be readable under the mailbox's own password alone, which is
	// what proves the rewrite happened rather than the legacy path being taken
	// forever.
	id, ok, err := st.GetSmimeIdentity()
	if err != nil || !ok {
		t.Fatal("the identity disappeared")
	}
	password, err := smimeStorePassword(st, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := smime.ParseIdentity(id.P12, password); err != nil {
		t.Errorf("the identity was not migrated to the per-mailbox password: %v", err)
	}
}
