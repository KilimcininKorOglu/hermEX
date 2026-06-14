package smime

import (
	"bytes"
	"crypto/x509"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestEncryptDecryptRoundTrip is the in-package check: an enveloped entity
// decrypts to the byte-identical inner content with the right key, and fails with
// the wrong key.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	id := newIdentity(t)
	inner := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\nSecret message body.\r\n")

	enc, err := Encrypt(inner, []*x509.Certificate{id.cert})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(enc, id.cert, id.key)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, inner) {
		t.Errorf("recovered content mismatch:\n got %q\nwant %q", got, inner)
	}

	// A different identity's key must not decrypt it.
	other := newIdentity(t)
	if _, err := Decrypt(enc, other.cert, other.key); err == nil {
		t.Error("Decrypt succeeded with the wrong key")
	}
}

// TestEncryptThenOpensslDecrypt proves openssl can decrypt our enveloped output.
func TestEncryptThenOpensslDecrypt(t *testing.T) {
	requireOpenssl(t)
	id := newIdentity(t)
	inner := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\nenvelope interop marker\r\n")

	enc, err := Encrypt(inner, []*x509.Certificate{id.cert})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "enc.eml")
	if err := os.WriteFile(in, enc, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("openssl", "smime", "-decrypt", "-in", in, "-recip", id.certPath, "-inkey", id.keyPath).CombinedOutput()
	if err != nil {
		t.Fatalf("openssl could not decrypt our envelope: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("envelope interop marker")) {
		t.Errorf("openssl decrypt output missing marker: %q", out)
	}
}

// TestOpensslEncryptThenDecrypt proves we decrypt an envelope produced by openssl
// with AES-256-CBC — the inbound interop direction.
func TestOpensslEncryptThenDecrypt(t *testing.T) {
	requireOpenssl(t)
	id := newIdentity(t)
	dir := t.TempDir()
	innerPath := filepath.Join(dir, "inner.txt")
	if err := os.WriteFile(innerPath, []byte("openssl enveloped this marker\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	encPath := filepath.Join(dir, "enc.eml")
	cmd := exec.Command("openssl", "smime", "-encrypt", "-aes-256-cbc", "-in", innerPath, "-out", encPath, id.certPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("openssl encrypt: %v\n%s", err, out)
	}
	enc, err := os.ReadFile(encPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(enc, id.cert, id.key)
	if err != nil {
		t.Fatalf("Decrypt of openssl envelope failed: %v", err)
	}
	if !bytes.Contains(got, []byte("openssl enveloped this marker")) {
		t.Errorf("recovered content missing marker: %q", got)
	}
}
