package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/dkimsign"
	"hermex/internal/mta"
	"hermex/internal/relay"
)

// forwardSigner counts the author signatures the spool would add.
type forwardSigner struct{ calls int }

func (f *forwardSigner) Sign(b []byte) []byte { f.calls++; return b }

// ownerKey is a KeyProvider with one key for the domain local.
type ownerKey []byte

func (k ownerKey) DKIMKey(domain string) ([]byte, string, bool, error) {
	return k, "s1", domain == "local", nil
}

// TestRuleForwardIsNotAuthorSigned queues an inbox-rule forward of a message
// that arrived claiming From: ceo@local. The bytes are the sender's, so the
// author signer must not run: it keys on the From domain and would sign the
// forgery as the hosted domain. The owner's domain seals the forward with ARC
// (RFC 8617) instead, the owner stays the envelope sender, and every recipient
// is queued.
func TestRuleForwardIsNotAuthorSigned(t *testing.T) {
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	fs := &forwardSigner{}
	sp.Signer = fs
	pem, _, err := dkimsign.GenerateKey(dkimsign.KeyRSA)
	if err != nil {
		t.Fatal(err)
	}
	fwd := &mta.Forwarding{Keys: ownerKey(pem), AuthServID: "mx.local",
		Lookup: func(string) ([]string, error) { return nil, errors.New("no DNS in tests") }}

	now := time.Now()
	raw := []byte("From: ceo@local\r\nSubject: wire money\r\n\r\nbody\r\n")
	if err := forwardEnqueue(sp, fwd)("owner@local", []string{"a@remote.test", "b@remote.test"}, raw, now); err != nil {
		t.Fatal(err)
	}
	if fs.calls != 0 {
		t.Errorf("a rule forward was author-signed %d times", fs.calls)
	}
	items, err := sp.Claim(now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("queued %d deliveries, want one per recipient", len(items))
	}
	for _, it := range items {
		if it.From != "owner@local" || !strings.HasPrefix(string(it.Body), "ARC-Seal: i=1; a=rsa-sha256; d=local;") {
			t.Errorf("forward to %s: envelope %q, sealed by the owner's domain %v", it.Recipient, it.From,
				strings.HasPrefix(string(it.Body), "ARC-Seal: i=1; a=rsa-sha256; d=local;"))
		}
	}
}
