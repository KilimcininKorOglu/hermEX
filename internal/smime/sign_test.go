package smime

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// testIdentity is a self-signed RSA certificate and key for tests, written to PEM
// files so openssl can use the same identity for interop checks.
type testIdentity struct {
	cert     *x509.Certificate
	key      *rsa.PrivateKey
	certPath string
	keyPath  string
}

// newIdentity builds a self-signed email-protection certificate (CN/SAN
// alice@hermex.test) and writes cert.pem and key.pem into a temp dir.
func newIdentity(t *testing.T) testIdentity {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber:   big.NewInt(1),
		Subject:        pkix.Name{CommonName: "alice@hermex.test"},
		NotBefore:      time.Now().Add(-time.Hour),
		NotAfter:       time.Now().Add(24 * time.Hour),
		KeyUsage:       x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
		EmailAddresses: []string{"alice@hermex.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	id := testIdentity{cert: cert, key: key, certPath: filepath.Join(dir, "cert.pem"), keyPath: filepath.Join(dir, "key.pem")}
	writePEM(t, id.certPath, "CERTIFICATE", der)
	writePEM(t, id.keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
	return id
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireOpenssl(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not on PATH; skipping S/MIME interop test")
	}
}

// TestSignVerifyRoundTrip is the in-package check: a signed entity verifies, the
// recovered content is byte-identical to the input, and the signer certificate is
// returned.
func TestSignVerifyRoundTrip(t *testing.T) {
	id := newIdentity(t)
	content := []byte("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 7bit\r\n\r\nHello S/MIME world.\r\n")

	signed, err := Sign(content, id.cert, id.key)
	if err != nil {
		t.Fatal(err)
	}
	signer, got, err := Verify(signed)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("recovered content mismatch:\n got %q\nwant %q", got, content)
	}
	if signer == nil || signer.Subject.CommonName != "alice@hermex.test" {
		t.Errorf("signer = %v, want CN alice@hermex.test", signer)
	}

	// A tampered body must fail verification.
	tampered := bytes.Replace(signed, []byte("Hello S/MIME world."), []byte("Hello EVIL world.."), 1)
	if _, _, err := Verify(tampered); err == nil {
		t.Error("Verify accepted a tampered message")
	}
}

// TestSignThenOpensslVerify proves our signed output verifies under openssl — the
// interop oracle, not our own verifier.
func TestSignThenOpensslVerify(t *testing.T) {
	requireOpenssl(t)
	id := newIdentity(t)
	content := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\nInterop signed body.\r\n")

	signed, err := Sign(content, id.cert, id.key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "signed.eml")
	if err := os.WriteFile(in, signed, 0o600); err != nil {
		t.Fatal(err)
	}
	// -noverify skips signer-certificate chain validation (the cert is self-signed);
	// the signature itself is still checked.
	cmd := exec.Command("openssl", "smime", "-verify", "-in", in, "-noverify", "-certfile", id.certPath, "-out", filepath.Join(dir, "out.txt"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("openssl rejected our signature: %v\n%s", err, out)
	}
}

// TestOpensslSignThenVerify proves we verify a signature produced by openssl —
// the inbound interop direction.
func TestOpensslSignThenVerify(t *testing.T) {
	requireOpenssl(t)
	id := newIdentity(t)
	dir := t.TempDir()
	payload := filepath.Join(dir, "payload.txt")
	if err := os.WriteFile(payload, []byte("openssl signed this payload marker\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	signedPath := filepath.Join(dir, "signed.eml")
	cmd := exec.Command("openssl", "smime", "-sign", "-md", "sha256", "-signer", id.certPath, "-inkey", id.keyPath, "-in", payload, "-out", signedPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("openssl sign: %v\n%s", err, out)
	}
	signed, err := os.ReadFile(signedPath)
	if err != nil {
		t.Fatal(err)
	}
	signer, content, err := Verify(signed)
	if err != nil {
		t.Fatalf("Verify of openssl-signed message failed: %v", err)
	}
	if signer == nil {
		t.Fatal("no signer certificate returned")
	}
	if !bytes.Contains(content, []byte("payload marker")) {
		t.Errorf("recovered content missing payload: %q", content)
	}
}
