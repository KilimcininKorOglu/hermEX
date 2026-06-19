package activesync

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/wbxml"
)

// genCert creates a certificate for cn valid in [notBefore, notAfter], signed by
// parent/parentKey (self-signed when parent is nil). A CA can sign other certs; a
// leaf carries the S/MIME (email-protection) extended key usage.
func genCert(t *testing.T, cn string, notBefore, notAfter time.Time, isCA bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	if isCA {
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, der
}

// certServer starts an ActiveSync server whose ValidateCert trust anchors are the
// given pool.
func certServer(t *testing.T, roots *x509.CertPool) *httptest.Server {
	t.Helper()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: filepath.Join(t.TempDir(), "mbox")}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	srv.roots = roots
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// validateCertReq builds a ValidateCert request carrying the given base64-DER
// certificates as the set to validate.
func validateCertReq(certsB64 ...string) *wbxml.Node {
	certs := make([]*wbxml.Node, 0, len(certsB64))
	for _, c := range certsB64 {
		certs = append(certs, wbxml.Str(wbxml.VCCertificate, c))
	}
	return wbxml.Elem(wbxml.VCValidateCert, wbxml.Elem(wbxml.VCCertificates, certs...))
}

// firstCertStatus returns the per-certificate Status from a ValidateCert reply.
func firstCertStatus(root *wbxml.Node) string {
	cert := root.Child(wbxml.VCCertificate)
	if cert == nil {
		return ""
	}
	return cert.ChildText(wbxml.VCStatus)
}

func b64DER(der []byte) string { return base64.StdEncoding.EncodeToString(der) }

// TestValidateCertTrusted proves a certificate chaining to a trusted root and
// valid for S/MIME signing validates with Status 1.
func TestValidateCertTrusted(t *testing.T) {
	now := time.Now()
	ca, caKey, _ := genCert(t, "Test CA", now.Add(-time.Hour), now.Add(24*time.Hour), true, nil, nil)
	_, _, leafDER := genCert(t, "alice@hermex.test", now.Add(-time.Hour), now.Add(24*time.Hour), false, ca, caKey)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	ts := certServer(t, pool)

	_, root := postCommand(t, ts, "ValidateCert", validateCertReq(b64DER(leafDER)))
	if s := root.ChildText(wbxml.VCStatus); s != "1" {
		t.Errorf("overall Status = %q, want 1", s)
	}
	if s := firstCertStatus(root); s != "1" {
		t.Errorf("certificate Status = %q, want 1 (validated)", s)
	}
}

// TestValidateCertUntrusted proves a certificate that does not chain to a trusted
// root reports Status 4.
func TestValidateCertUntrusted(t *testing.T) {
	now := time.Now()
	_, _, der := genCert(t, "stranger", now.Add(-time.Hour), now.Add(24*time.Hour), false, nil, nil)
	ts := certServer(t, x509.NewCertPool())

	_, root := postCommand(t, ts, "ValidateCert", validateCertReq(b64DER(der)))
	if s := firstCertStatus(root); s != "4" {
		t.Errorf("certificate Status = %q, want 4 (untrusted)", s)
	}
}

// TestValidateCertExpired proves an expired certificate (even from a trusted CA)
// reports the bad-time Status 8.
func TestValidateCertExpired(t *testing.T) {
	now := time.Now()
	ca, caKey, _ := genCert(t, "Test CA", now.Add(-48*time.Hour), now.Add(24*time.Hour), true, nil, nil)
	_, _, leafDER := genCert(t, "old", now.Add(-48*time.Hour), now.Add(-24*time.Hour), false, ca, caKey)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	ts := certServer(t, pool)

	_, root := postCommand(t, ts, "ValidateCert", validateCertReq(b64DER(leafDER)))
	if s := firstCertStatus(root); s != "8" {
		t.Errorf("certificate Status = %q, want 8 (expired)", s)
	}
}

// TestValidateCertMalformed proves an unparseable certificate reports Status 3.
func TestValidateCertMalformed(t *testing.T) {
	ts := certServer(t, x509.NewCertPool())

	_, root := postCommand(t, ts, "ValidateCert", validateCertReq("not-a-certificate"))
	if s := firstCertStatus(root); s != "3" {
		t.Errorf("certificate Status = %q, want 3 (cannot validate)", s)
	}
}

// TestValidateCertEmpty proves a request naming no certificate reports the
// protocol-error Status 2.
func TestValidateCertEmpty(t *testing.T) {
	ts := certServer(t, x509.NewCertPool())

	_, root := postCommand(t, ts, "ValidateCert", wbxml.Elem(wbxml.VCValidateCert))
	if s := root.ChildText(wbxml.VCStatus); s != "2" {
		t.Errorf("overall Status = %q, want 2 (protocol error)", s)
	}
}
