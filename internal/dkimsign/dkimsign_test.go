package dkimsign

import (
	"bytes"
	"fmt"
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
	privPEM, dnsTXT, err := GenerateKey()
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

// TestSignFailsOpen proves signing never blocks delivery: a domain with no key, and a
// stored key that is malformed, both yield the original message unchanged.
func TestSignFailsOpen(t *testing.T) {
	privPEM, _, _ := GenerateKey()

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
