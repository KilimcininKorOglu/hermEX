package dkimsign

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"
)

// fakeKeys serves one domain's key.
type fakeKeys struct {
	domain   string
	privPEM  []byte
	selector string
	err      error
}

func (f fakeKeys) DKIMKey(domain string) ([]byte, string, bool, error) {
	if f.err != nil {
		return nil, "", false, f.err
	}
	if domain == f.domain {
		return f.privPEM, f.selector, true, nil
	}
	return nil, "", false, nil
}

const testMsg = "From: Alice <alice@example.com>\r\n" +
	"To: bob@remote.test\r\n" +
	"Subject: hello there\r\n" +
	"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
	"Message-ID: <m1@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"BODYTOKEN this is the message body\r\n"

// TestSignVerifies is the load-bearing test: a signed message verifies against the
// generated public key, tampering the body breaks it, and d= is the From-header domain.
// The LookupTXT only answers for sel1._domainkey.example.com, so a wrong d= or selector
// would itself fail verification.
func TestSignVerifies(t *testing.T) {
	privPEM, dnsTXT, err := GenerateKey(KeyRSA)
	if err != nil {
		t.Fatal(err)
	}
	s := &Signer{Keys: fakeKeys{domain: "example.com", privPEM: privPEM, selector: "sel1"}}
	signed := s.Sign([]byte(testMsg))
	if bytes.Equal(signed, []byte(testMsg)) {
		t.Fatal("message was not signed")
	}

	lookup := func(name string) ([]string, error) {
		if name == "sel1._domainkey.example.com" {
			return []string{dnsTXT}, nil
		}
		return nil, fmt.Errorf("unexpected DKIM lookup %q (wrong d= or selector)", name)
	}
	verify := func(raw []byte) []*dkim.Verification {
		vs, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{LookupTXT: lookup})
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		return vs
	}

	vs := verify(signed)
	if len(vs) != 1 {
		t.Fatalf("got %d verifications, want 1", len(vs))
	}
	if vs[0].Err != nil {
		t.Errorf("signed message must verify, got %v", vs[0].Err)
	}
	if vs[0].Domain != "example.com" {
		t.Errorf("d= = %q, want the From-header domain example.com", vs[0].Domain)
	}

	// Tampering one body byte must break the body hash → verification fails.
	tampered := bytes.Replace(signed, []byte("BODYTOKEN"), []byte("TAMPERED!"), 1)
	if vt := verify(tampered); vt[0].Err == nil {
		t.Error("a tampered body must fail DKIM verification")
	}
}

// TestSignVerifiesEd25519 proves the Ed25519 option is a working signing key, not just a
// different record: the signer picks the algorithm from the stored key, and the published
// record verifies the signature it produced.
func TestSignVerifiesEd25519(t *testing.T) {
	privPEM, dnsTXT, err := GenerateKey(KeyEd25519)
	if err != nil {
		t.Fatal(err)
	}
	s := &Signer{Keys: fakeKeys{domain: "example.com", privPEM: privPEM, selector: "sel1"}}
	signed := s.Sign([]byte(testMsg))
	if bytes.Equal(signed, []byte(testMsg)) {
		t.Fatal("message was not signed")
	}
	lookup := func(name string) ([]string, error) {
		if name == "sel1._domainkey.example.com" {
			return []string{dnsTXT}, nil
		}
		return nil, fmt.Errorf("unexpected DKIM lookup %q (wrong d= or selector)", name)
	}
	vs, err := dkim.VerifyWithOptions(bytes.NewReader(signed), &dkim.VerifyOptions{LookupTXT: lookup})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(vs) != 1 {
		t.Fatalf("got %d verifications, want 1", len(vs))
	}
	if vs[0].Err != nil {
		t.Errorf("an Ed25519-signed message must verify, got %v", vs[0].Err)
	}
}

// TestGenerateKeyTagsRSA proves the RSA record names its own algorithm.
func TestGenerateKeyTagsRSA(t *testing.T) {
	_, dnsTXT, err := GenerateKey(KeyRSA)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dnsTXT, "k=rsa") {
		t.Errorf("record = %q, want a k=rsa tag", dnsTXT)
	}
}

// TestGenerateKeyDefaultsToRSA proves an empty key type is the interoperable default, so a
// form that omits the field keeps producing the key every receiver verifies.
func TestGenerateKeyDefaultsToRSA(t *testing.T) {
	_, dnsTXT, err := GenerateKey("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dnsTXT, "k=rsa") {
		t.Errorf("record = %q, want a k=rsa tag", dnsTXT)
	}
}

// TestGenerateKeyTagsEd25519 proves the Ed25519 record names its own algorithm.
func TestGenerateKeyTagsEd25519(t *testing.T) {
	_, dnsTXT, err := GenerateKey(KeyEd25519)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dnsTXT, "k=ed25519") {
		t.Errorf("record = %q, want a k=ed25519 tag", dnsTXT)
	}
}

// TestGenerateKeyEd25519PublishesRawKey is the shape that a verifier enforces: RFC 8463
// puts the raw 32-byte public key in p=, and a DER-wrapped one is rejected outright.
func TestGenerateKeyEd25519PublishesRawKey(t *testing.T) {
	_, dnsTXT, err := GenerateKey(KeyEd25519)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(PublicKeyPayload(dnsTXT))
	if err != nil {
		t.Fatalf("p= is not base64: %v", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		t.Errorf("p= decodes to %d bytes, want the raw %d-byte public key", len(raw), ed25519.PublicKeySize)
	}
}

// TestGenerateKeyRejectsUnknownType proves an unknown algorithm is refused rather than
// silently answered with an RSA key the operator did not ask for.
func TestGenerateKeyRejectsUnknownType(t *testing.T) {
	if _, _, err := GenerateKey("dsa"); err == nil {
		t.Error("an unknown key type must be an error")
	}
}

// TestPublicKeyPayload covers the p= extraction the panel copies as a bare key.
func TestPublicKeyPayload(t *testing.T) {
	if got := PublicKeyPayload("v=DKIM1; k=rsa; p=ABCDEF"); got != "ABCDEF" {
		t.Errorf("payload = %q, want ABCDEF", got)
	}
}

// TestPublicKeyPayloadWithoutTag proves a record carrying no p= yields an empty string
// instead of a wrong tag's value.
func TestPublicKeyPayloadWithoutTag(t *testing.T) {
	if got := PublicKeyPayload("v=DKIM1; k=rsa"); got != "" {
		t.Errorf("payload = %q, want an empty string", got)
	}
}

// TestSignFailsOpen proves signing never blocks delivery: a domain with no key, and a
// stored key that is malformed, both yield the original message unchanged.
func TestSignFailsOpen(t *testing.T) {
	privPEM, _, _ := GenerateKey(KeyRSA)

	t.Run("no key for domain", func(t *testing.T) {
		s := &Signer{Keys: fakeKeys{domain: "other.test", privPEM: privPEM, selector: "s"}}
		if got := s.Sign([]byte(testMsg)); !bytes.Equal(got, []byte(testMsg)) {
			t.Error("a message from a domain with no key must be returned unsigned")
		}
	})
	t.Run("malformed key", func(t *testing.T) {
		s := &Signer{Keys: fakeKeys{domain: "example.com", privPEM: []byte("not a pem key"), selector: "s"}}
		if got := s.Sign([]byte(testMsg)); !bytes.Equal(got, []byte(testMsg)) {
			t.Error("a malformed key must yield the original message, not a failed delivery")
		}
	})
	t.Run("lookup error", func(t *testing.T) {
		s := &Signer{Keys: fakeKeys{err: fmt.Errorf("db down")}}
		if got := s.Sign([]byte(testMsg)); !bytes.Equal(got, []byte(testMsg)) {
			t.Error("a key-lookup error must yield the original message")
		}
	})
}

// TestFromHeaderDomain covers the d= source: the first From address's domain,
// lower-cased, and empty for a missing or unparseable header.
func TestFromHeaderDomain(t *testing.T) {
	cases := []struct{ in, want string }{
		{"From: Alice <alice@example.com>\r\n\r\nbody", "example.com"},
		{"From: bob@MixedCase.COM\r\n\r\nbody", "mixedcase.com"},
		{"Subject: no from\r\n\r\nbody", ""},
		{"From: not-an-address\r\n\r\nbody", ""},
	}
	for _, c := range cases {
		if got := fromHeaderDomain([]byte(c.in)); got != c.want {
			t.Errorf("fromHeaderDomain(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
