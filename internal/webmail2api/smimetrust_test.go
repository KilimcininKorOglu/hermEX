package webmail2api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/smime"
)

// selfSigned mints a self-signed S/MIME certificate asserting the given address,
// which is exactly what an attacker can produce with two openssl commands.
func selfSigned(t *testing.T, address string, notBefore, notAfter time.Time) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:   big.NewInt(1),
		Subject:        pkix.Name{CommonName: address},
		EmailAddresses: []string{address},
		NotBefore:      notBefore,
		NotAfter:       notAfter,
		KeyUsage:       x509.KeyUsageDigitalSignature,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// signMessage wraps a body in a multipart/signed entity signed with the given identity.
func signMessage(t *testing.T, cert *x509.Certificate, key *rsa.PrivateKey) []byte {
	t.Helper()
	content := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\nWire the money to account 42.\r\n")
	signed, err := smime.Sign(content, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// trustHarness builds a server whose only local account is alice, with an empty
// mailbox, and returns the server plus alice's mailbox path.
func trustHarness(t *testing.T) (*Server, string) {
	t.Helper()
	mbox := t.TempDir()
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mbox}}
	return NewServer(accounts, accounts, nil, "mail.hermex.test", []byte("smime-trust-secret"), "", false), mbox
}

// publish stores cert as the mailbox owner's own published S/MIME certificate,
// the record a sender fetches to encrypt to them and the one the pinned trust
// route matches against.
func publish(t *testing.T, mbox string, cert *x509.Certificate) {
	t.Helper()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetSmimeIdentity(objectstore.SmimeIdentity{Mode: "browser", Cert: cert.Raw}); err != nil {
		t.Fatal(err)
	}
}

// TestSmimeSelfSignedUnknownSignerIsNotVerified is the core attack: anyone who can
// deliver a message mints a certificate carrying the victim's address, signs with
// it, and the signature checks out mathematically. Nothing about that ties the key
// to the victim, so the reader must not be shown a verified badge.
func TestSmimeSelfSignedUnknownSignerIsNotVerified(t *testing.T) {
	srv, _ := trustHarness(t)
	cert, key := selfSigned(t, "alice@hermex.test", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))

	verified, signedBy := srv.smimeStatusFor(signMessage(t, cert, key), "alice@hermex.test")
	if verified {
		t.Error("a self-signed certificate nobody vouches for was presented as verified")
	}
	if signedBy != "alice@hermex.test" {
		t.Errorf("signedBy = %q, want the address the certificate claimed", signedBy)
	}
}

// TestSmimePinnedCertificateIsVerified covers the internal case: the sender
// published this exact certificate through their own authenticated session, so the
// server has a reason beyond the signature itself to attribute it to them.
func TestSmimePinnedCertificateIsVerified(t *testing.T) {
	srv, mbox := trustHarness(t)
	cert, key := selfSigned(t, "alice@hermex.test", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	publish(t, mbox, cert)

	verified, signedBy := srv.smimeStatusFor(signMessage(t, cert, key), "alice@hermex.test")
	if !verified {
		t.Error("the sender's own published certificate was not trusted")
	}
	if signedBy != "alice@hermex.test" {
		t.Errorf("signedBy = %q, want alice@hermex.test", signedBy)
	}
}

// TestSmimeSignerAddressMismatchIsNotVerified proves the identity bind runs before
// trust: a certificate that is pinned but asserts a different address still cannot
// speak for the From address.
func TestSmimeSignerAddressMismatchIsNotVerified(t *testing.T) {
	srv, mbox := trustHarness(t)
	cert, key := selfSigned(t, "mallory@evil.test", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	publish(t, mbox, cert)

	if verified, _ := srv.smimeStatusFor(signMessage(t, cert, key), "alice@hermex.test"); verified {
		t.Error("a certificate claiming another address was attributed to the From address")
	}
}

// TestSmimeExpiredPinnedCertificateIsNotVerified proves the pinned route enforces
// the validity window, which x509.Verify would have enforced on the chain route.
func TestSmimeExpiredPinnedCertificateIsNotVerified(t *testing.T) {
	srv, mbox := trustHarness(t)
	cert, key := selfSigned(t, "alice@hermex.test", time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	publish(t, mbox, cert)

	if verified, _ := srv.smimeStatusFor(signMessage(t, cert, key), "alice@hermex.test"); verified {
		t.Error("an expired certificate was trusted")
	}
}

// TestSmimeUnparseableSignatureIsNotVerified confirms a failed signature check
// reports nothing at all, not an empty-but-verified status.
func TestSmimeUnparseableSignatureIsNotVerified(t *testing.T) {
	srv, _ := trustHarness(t)
	verified, signedBy := srv.smimeStatusFor([]byte("Content-Type: text/plain\r\n\r\nnot signed at all\r\n"), "alice@hermex.test")
	if verified || signedBy != "" {
		t.Errorf("unsigned content reported verified=%v signedBy=%q", verified, signedBy)
	}
}

// TestCertAssertsAddress pins the matching rules: SAN and address-shaped common
// names count, a display-name common name never does, and case is ignored.
func TestCertAssertsAddress(t *testing.T) {
	cert, _ := selfSigned(t, "alice@hermex.test", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if !certAssertsAddress(cert, "ALICE@Hermex.Test") {
		t.Error("case-insensitive match failed")
	}
	if certAssertsAddress(cert, "bob@hermex.test") {
		t.Error("an unrelated address matched")
	}
	if certAssertsAddress(cert, "") {
		t.Error("an empty address matched")
	}
	display := &x509.Certificate{Subject: pkix.Name{CommonName: "Alice Example"}}
	if certAssertsAddress(display, "Alice Example") {
		t.Error("a display-name common name was accepted as an address claim")
	}
}
