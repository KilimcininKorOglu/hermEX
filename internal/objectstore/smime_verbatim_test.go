package objectstore

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/smime"
)

// TestSMIMEVerbatimPreservation checks that signed and encrypted S/MIME messages
// are served byte-for-byte (their original is preserved, not re-synthesized),
// while a normal message keeps the regenerated path and stores no original.
func TestSMIMEVerbatimPreservation(t *testing.T) {
	s := openSeededStore(t)

	signed := []byte("From: a@b.test\r\nTo: c@b.test\r\nSubject: signed\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=\"sha-256\"; boundary=\"bnd\"\r\n\r\n" +
		"--bnd\r\nContent-Type: text/plain\r\n\r\nHello signed.\r\n" +
		"--bnd\r\nContent-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\nContent-Transfer-Encoding: base64\r\n\r\nQk9HVVM=\r\n--bnd--\r\n")
	encrypted := []byte("From: a@b.test\r\nTo: c@b.test\r\nSubject: secret\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=\"smime.p7m\"\r\n" +
		"Content-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"smime.p7m\"\r\n\r\nQk9HVVM=\r\n")

	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"signed", signed},
		{"encrypted", encrypted},
	} {
		info, err := s.AppendMessage(mapi.PrivateFIDInbox, tc.raw, time.Unix(1700000000, 0), 0)
		if err != nil {
			t.Fatalf("%s: append: %v", tc.name, err)
		}
		got, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
		if err != nil {
			t.Fatalf("%s: get raw: %v", tc.name, err)
		}
		if !bytes.Equal(got, tc.raw) {
			t.Errorf("%s: served form is not byte-identical to arrival\n got %q\nwant %q", tc.name, got, tc.raw)
		}
	}

	// A normal message is re-synthesized as before and stores no original.
	normal := []byte("From: a@b.test\r\nTo: c@b.test\r\nSubject: plain\r\n\r\njust text\r\n")
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, normal, time.Unix(1700000001, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	props, err := s.GetMessageProperties(info.ID, mapi.PrSmimeOriginal)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := props.Get(mapi.PrSmimeOriginal); ok {
		t.Error("a normal message must not store an S/MIME original")
	}
}

// TestSMIMESignedSurvivesStore is the end-to-end proof: a genuinely signed
// message, once stored, is still served byte-for-byte and still verifies — even
// after the regenerable eml cache is dropped, so the durable preserved original
// (not just the cache) carries the signature.
func TestSMIMESignedSurvivesStore(t *testing.T) {
	s := openSeededStore(t)
	cert, key := genSignerCert(t)

	content := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\nStored and still signed.\r\n")
	body, err := smime.Sign(content, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	msg := append([]byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: signed\r\n"), body...)

	info, err := s.AppendMessage(mapi.PrivateFIDInbox, msg, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	// Drop the eml cache so the read must rebuild from the preserved original.
	if err := os.Remove(s.emlPath(midString(uint64(info.ID)))); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, msg) {
		t.Error("served signed message differs from the stored bytes")
	}
	if _, _, err := smime.Verify(got); err != nil {
		t.Errorf("stored signed message no longer verifies: %v", err)
	}
}

// genSignerCert builds a self-signed email-protection certificate and key.
func genSignerCert(t *testing.T) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "alice@hermex.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
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
	return cert, key
}
