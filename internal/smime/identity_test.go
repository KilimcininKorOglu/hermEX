package smime

import (
	"bytes"
	"encoding/pem"
	"testing"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// TestParseIdentityOpensTheUploadedContainer encodes an identity the way openssl 3
// does by default (AES-based PKCS#12, RFC 7292) and reads it back with its
// password: the key and the certificate are the ones that went in.
func TestParseIdentityOpensTheUploadedContainer(t *testing.T) {
	id := newIdentity(t)
	p12, err := pkcs12.Modern.Encode(id.key, id.cert, nil, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	key, cert, err := ParseIdentity(p12, "s3cret")
	if err != nil {
		t.Fatalf("ParseIdentity: %v", err)
	}
	if !cert.Equal(id.cert) {
		t.Error("the certificate read back is not the one encoded")
	}
	if !id.key.Equal(key) {
		t.Error("the private key read back is not the one encoded")
	}
}

// TestParseIdentityRefusesAWrongPasswordOrGarbage is the hostile upload: a
// container under another password, a truncated one, and bytes that are no
// PKCS#12 at all. Each must be refused and hand back neither key nor certificate.
func TestParseIdentityRefusesAWrongPasswordOrGarbage(t *testing.T) {
	id := newIdentity(t)
	p12, err := pkcs12.Modern.Encode(id.key, id.cert, nil, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		data []byte
		pw   string
	}{
		"wrong password": {p12, "guess"},
		"empty password": {p12, ""},
		"truncated":      {p12[:len(p12)/2], "s3cret"},
		"garbage":        {[]byte("not a pkcs12 container"), "s3cret"},
		"empty":          {nil, "s3cret"},
	} {
		key, cert, err := ParseIdentity(c.data, c.pw)
		if err == nil {
			t.Errorf("%s: ParseIdentity accepted it", name)
		}
		if key != nil || cert != nil {
			t.Errorf("%s: a refused container still returned a key or certificate", name)
		}
	}
}

// TestParseCertificateReadsPEMAndDER imports a recipient certificate in the two
// forms clients export (RFC 7468 PEM, and raw DER) and with explanatory text
// around the PEM block, which RFC 7468 2 allows; each is the same certificate.
func TestParseCertificateReadsPEMAndDER(t *testing.T) {
	id := newIdentity(t)
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: id.cert.Raw})
	for name, data := range map[string][]byte{
		"der":             id.cert.Raw,
		"pem":             block,
		"pem with a note": append(append([]byte("Subject: alice\n"), block...), "trailing note\n"...),
	} {
		cert, err := ParseCertificate(data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !cert.Equal(id.cert) {
			t.Errorf("%s: parsed a different certificate", name)
		}
	}
}

// TestParseCertificateRefusesWhatIsNoCertificate uploads a private key in PEM, a
// PEM block holding noise, a truncated DER and plain text. None is a certificate,
// and none may be stored as a recipient's encryption certificate.
func TestParseCertificateRefusesWhatIsNoCertificate(t *testing.T) {
	id := newIdentity(t)
	for name, data := range map[string][]byte{
		"private key": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: bytes.Repeat([]byte{1}, 64)}),
		"pem noise":   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("noise")}),
		"short der":   id.cert.Raw[:len(id.cert.Raw)/2],
		"text":        []byte("hello"),
		"empty":       nil,
	} {
		if cert, err := ParseCertificate(data); err == nil || cert != nil {
			t.Errorf("%s: ParseCertificate = %v, %v; want an error and no certificate", name, cert, err)
		}
	}
}
