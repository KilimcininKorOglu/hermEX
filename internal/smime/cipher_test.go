package smime

import (
	"bytes"
	"crypto/x509"
	"encoding/asn1"
	"testing"

	"github.com/smallstep/pkcs7"
)

// envelopeNames reports whether an enveloped entity this package produced carries
// the given content-encryption algorithm, read straight out of its DER.
func envelopeNames(t *testing.T, enc []byte, oid asn1.ObjectIdentifier) bool {
	t.Helper()
	_, body, ok := bytes.Cut(canonicalizeCRLF(enc), []byte("\r\n\r\n"))
	if !ok {
		t.Fatal("the envelope has no body")
	}
	der, err := decodeBase64Body(body)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := asn1.Marshal(oid)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Contains(der, encoded)
}

// TestDecryptFollowsTheSendersCipher is the fact that decides what the content
// cipher choice can and cannot protect: the enveloped-data decryptor picks its
// algorithm from the OID the incoming message carries, not from what this server
// would have chosen. Every S/MIME client in the field sends AES-CBC, so the CBC
// decrypt path stays reachable however this server encrypts, and changing the
// outgoing cipher narrows nothing on the receiving side.
func TestDecryptFollowsTheSendersCipher(t *testing.T) {
	id := newIdentity(t)
	inner := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\ncipher-agility marker\r\n")

	// Stand in for a correspondent whose client enveloped with a different
	// cipher than ours, and restore the package setting for the other tests.
	restore := pkcs7.ContentEncryptionAlgorithm
	pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES256GCM
	t.Cleanup(func() { pkcs7.ContentEncryptionAlgorithm = restore })

	enc, err := Encrypt(inner, []*x509.Certificate{id.cert})
	if err != nil {
		t.Fatal(err)
	}
	// Without this the test would prove nothing: if the setting were ignored the
	// envelope would be CBC again and opening it would say nothing about agility.
	if !envelopeNames(t, enc, pkcs7.OIDEncryptionAlgorithmAES256GCM) {
		t.Fatal("the envelope is not GCM, so this test would not exercise a second cipher")
	}
	if envelopeNames(t, enc, pkcs7.OIDEncryptionAlgorithmAES256CBC) {
		t.Fatal("the envelope still names CBC")
	}

	// Back to what this server produces. The message must still open: the
	// decryptor reads the sender's algorithm, so it is not bound to ours.
	pkcs7.ContentEncryptionAlgorithm = restore
	got, err := Decrypt(enc, id.cert, id.key)
	if err != nil {
		t.Fatalf("a message enveloped with another cipher did not open: %v", err)
	}
	if !bytes.Equal(got, inner) {
		t.Errorf("recovered content mismatch:\n got %q\nwant %q", got, inner)
	}
}

// TestOutgoingCipherIsTheInteroperableOne pins the deliberate choice for what
// this server produces. AES-256-CBC is RFC 5751's mandatory-to-implement content
// cipher, so every S/MIME client can open what we send. A recipient whose client
// cannot open an envelope has no fallback and no way to tell us, which is a worse
// outcome than the AEAD this trades away.
func TestOutgoingCipherIsTheInteroperableOne(t *testing.T) {
	if pkcs7.ContentEncryptionAlgorithm != pkcs7.EncryptionAlgorithmAES256CBC {
		t.Errorf("outgoing content cipher = %d, want AES-256-CBC (%d): changing it can make our mail unreadable to a recipient we cannot detect",
			pkcs7.ContentEncryptionAlgorithm, pkcs7.EncryptionAlgorithmAES256CBC)
	}
}

// TestDecryptRevealsNothingAboutWhyItFailed is the property that keeps a padding
// oracle out of reach: every failure comes back as one opaque error, so a caller
// cannot tell a corrupted envelope from a wrong key from bad padding. The only
// caller discards the error entirely, but this pins the layer that would have to
// leak first.
func TestDecryptRevealsNothingAboutWhyItFailed(t *testing.T) {
	id := newIdentity(t)
	enc, err := Encrypt([]byte("Content-Type: text/plain\r\n\r\nbody\r\n"), []*x509.Certificate{id.cert})
	if err != nil {
		t.Fatal(err)
	}
	other := newIdentity(t)

	// A wrong key, and a corrupted envelope body, must be indistinguishable to
	// the caller: same error text, no detail about which check failed.
	_, wrongKey := Decrypt(enc, other.cert, other.key)
	corrupt := bytes.Replace(enc, []byte("MIME-Version: 1.0\r\n\r\n"), []byte("MIME-Version: 1.0\r\n\r\nAAAA"), 1)
	_, corrupted := Decrypt(corrupt, id.cert, id.key)

	if wrongKey == nil || corrupted == nil {
		t.Fatal("a bad decrypt succeeded")
	}
	for _, e := range []error{wrongKey, corrupted} {
		for _, leak := range []string{"padding", "pad", "block size", "authentication"} {
			if bytes.Contains(bytes.ToLower([]byte(e.Error())), []byte(leak)) {
				t.Errorf("the decrypt error names %q, which distinguishes why it failed: %v", leak, e)
			}
		}
	}
}
