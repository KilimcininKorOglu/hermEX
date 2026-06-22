package relay

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

// prefixSigner stands in for the DKIM signer, marking the body and recording the call
// count so a test can prove signing happens exactly once per message.
type prefixSigner struct{ calls int }

func (p *prefixSigner) Sign(body []byte) []byte {
	p.calls++
	return append([]byte("X-Signed: yes\r\n"), body...)
}

// TestEnqueueSignsOnce proves Enqueue signs the body a single time and stores that one
// signed form for every recipient — not once per recipient — so all copies carry the
// same valid signature.
func TestEnqueueSignsOnce(t *testing.T) {
	sp, err := Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	ps := &prefixSigner{}
	sp.Signer = ps

	now := time.Now()
	if err := sp.Enqueue("from@local", []string{"a@remote", "b@remote"}, []byte("Subject: hi\r\n\r\nbody"), now); err != nil {
		t.Fatal(err)
	}
	if ps.calls != 1 {
		t.Fatalf("signer called %d times, want exactly 1 (sign once, then fan out)", ps.calls)
	}
	items, err := sp.Claim(now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (one per recipient)", len(items))
	}
	for _, it := range items {
		if !bytes.HasPrefix(it.Body, []byte("X-Signed: yes")) {
			t.Errorf("recipient %s got an unsigned body: %q", it.Recipient, it.Body)
		}
	}
}

// TestEnqueueNilSignerUnchanged proves a spool without a signer stores the body
// untouched.
func TestEnqueueNilSignerUnchanged(t *testing.T) {
	sp, err := Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	now := time.Now()
	raw := []byte("Subject: hi\r\n\r\nbody")
	if err := sp.Enqueue("from@local", []string{"a@remote"}, raw, now); err != nil {
		t.Fatal(err)
	}
	items, _ := sp.Claim(now, 10)
	if len(items) != 1 || !bytes.Equal(items[0].Body, raw) {
		t.Errorf("a nil signer must leave the body unchanged, got %q", items[0].Body)
	}
}
