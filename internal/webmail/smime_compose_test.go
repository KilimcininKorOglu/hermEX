package webmail

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/smime"
)

// genWebmailIdentity builds a self-signed email certificate and key for tests.
func genWebmailIdentity(t *testing.T, cn string) (crypto.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}

func openSmimeStore(t *testing.T) *objectstore.Store {
	t.Helper()
	st, err := objectstore.Open(emptyMailbox(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestSplitForSmime checks the header partition: identity headers keep
// From/Subject and drop Content-Type/MIME-Version, which move into the inner
// entity with the body.
func TestSplitForSmime(t *testing.T) {
	raw := []byte("From: a@x\r\nTo: b@x\r\nSubject: hi\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nbody\r\n")
	ident, inner := splitForSmime(raw)
	if !bytes.Contains(ident, []byte("From: a@x")) || !bytes.Contains(ident, []byte("Subject: hi")) {
		t.Errorf("identity headers missing From/Subject: %q", ident)
	}
	if bytes.Contains(ident, []byte("Content-Type")) || bytes.Contains(ident, []byte("MIME-Version")) {
		t.Errorf("identity headers should not carry Content-Type/MIME-Version: %q", ident)
	}
	if !bytes.Contains(inner, []byte("Content-Type: text/plain")) || !bytes.Contains(inner, []byte("body")) {
		t.Errorf("inner entity missing content headers/body: %q", inner)
	}
}

// TestApplySmimeSign signs a message and confirms the outer keeps the identity
// headers, the body is multipart/signed, and the signature verifies.
func TestApplySmimeSign(t *testing.T) {
	srv := &Server{}
	key, cert := genWebmailIdentity(t, "alice@hermex.test")
	sess := &session{user: "alice@hermex.test", smimeKey: key, smimeCert: cert}
	raw := []byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: hi\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nHello body.\r\n")

	out, err := srv.applySmime(sess, nil, raw, []string{"bob@hermex.test"}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("From: alice@hermex.test")) || !bytes.Contains(out, []byte("multipart/signed")) {
		t.Errorf("signed message missing identity header or multipart/signed")
	}
	signer, inner, err := smime.Verify(out)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if signer == nil || !bytes.Contains(inner, []byte("Hello body.")) {
		t.Errorf("verify recovered wrong content: %q", inner)
	}
}

// TestApplySmimeEncrypt and sign+encrypt: the sender encrypts to a recipient and
// to itself, so it can decrypt the result; with both, decrypt yields the signed
// entity, which verifies.
func TestApplySmimeEncrypt(t *testing.T) {
	srv := &Server{}
	aliceKey, aliceCert := genWebmailIdentity(t, "alice@hermex.test")
	sess := &session{user: "alice@hermex.test", smimeKey: aliceKey, smimeCert: aliceCert}
	st := openSmimeStore(t)
	_, bobCert := genWebmailIdentity(t, "bob@hermex.test")
	if err := st.PutRecipientCert("bob@hermex.test", bobCert.Raw); err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: hi\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nSecret.\r\n")

	enc, err := srv.applySmime(sess, st, raw, []string{"bob@hermex.test"}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(enc, []byte("application/pkcs7-mime")) {
		t.Error("encrypted message is not application/pkcs7-mime")
	}
	dec, err := smime.Decrypt(enc, aliceCert, aliceKey) // sender decrypts its own (self-recipient) copy
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Contains(dec, []byte("Secret.")) {
		t.Errorf("decrypt lost the body: %q", dec)
	}

	both, err := srv.applySmime(sess, st, raw, []string{"bob@hermex.test"}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	signedInner, err := smime.Decrypt(both, aliceCert, aliceKey)
	if err != nil {
		t.Fatalf("Decrypt(sign+encrypt): %v", err)
	}
	signer, inner, err := smime.Verify(signedInner)
	if err != nil {
		t.Fatalf("Verify inner: %v", err)
	}
	if signer == nil || !bytes.Contains(inner, []byte("Secret.")) {
		t.Errorf("sign+encrypt inner content wrong: %q", inner)
	}
}

// TestApplySmimeErrors covers the guardrails: signing without an unlocked
// identity, and encrypting to a recipient with no stored certificate.
func TestApplySmimeErrors(t *testing.T) {
	srv := &Server{}
	st := openSmimeStore(t)
	raw := []byte("From: a@x\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nx")

	if _, err := srv.applySmime(&session{}, st, raw, []string{"b@x"}, true, false); err == nil {
		t.Error("signing without an unlocked identity should fail")
	}
	key, cert := genWebmailIdentity(t, "alice@hermex.test")
	sess := &session{smimeKey: key, smimeCert: cert}
	if _, err := srv.applySmime(sess, st, raw, []string{"nobody@x"}, false, true); err == nil {
		t.Error("encrypting without a recipient certificate should fail")
	}
}
