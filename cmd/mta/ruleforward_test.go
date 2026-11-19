package main

import (
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/relay"
)

// forwardSigner counts the author signatures the spool would add.
type forwardSigner struct{ calls int }

func (f *forwardSigner) Sign(b []byte) []byte { f.calls++; return b }

// TestRuleForwardIsNotAuthorSigned queues an inbox-rule forward of a message
// that arrived claiming From: ceo@local. The bytes are the sender's, so the
// author signer must not run: it keys on the From domain and would sign the
// forgery as the hosted domain. Every recipient is still queued.
func TestRuleForwardIsNotAuthorSigned(t *testing.T) {
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	fs := &forwardSigner{}
	sp.Signer = fs

	now := time.Now()
	raw := []byte("From: ceo@local\r\nSubject: wire money\r\n\r\nbody")
	if err := forwardEnqueue(sp)("owner@local", []string{"a@remote.test", "b@remote.test"}, raw, now); err != nil {
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
		t.Errorf("queued %d deliveries, want one per recipient", len(items))
	}
}
