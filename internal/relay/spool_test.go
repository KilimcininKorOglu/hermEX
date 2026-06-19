package relay

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

func openSpool(t *testing.T) *Spool {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestSpoolPerRecipientLifecycle proves the core durability contract: a
// submission to several externals is queued per recipient, a delivered recipient
// is settled without disturbing the others (the shared body survives until the
// last is gone), and a transient failure defers only that recipient.
func TestSpoolPerRecipientLifecycle(t *testing.T) {
	s := openSpool(t)
	t0 := time.Unix(1_000_000, 0)
	body := []byte("From: a@local\r\nSubject: hi\r\n\r\nbody\r\n")

	if err := s.Enqueue("a@local", []string{"x@remote", "y@remote"}, body, t0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	due, err := s.Claim(t0, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("claimed %d items, want 2 (one per recipient)", len(due))
	}
	byRcpt := map[string]Item{}
	for _, it := range due {
		if it.From != "a@local" {
			t.Errorf("item From = %q, want a@local", it.From)
		}
		if !bytes.Equal(it.Body, body) {
			t.Errorf("item Body not preserved for %s", it.Recipient)
		}
		if it.Attempts != 0 {
			t.Errorf("fresh item Attempts = %d, want 0", it.Attempts)
		}
		byRcpt[it.Recipient] = it
	}

	// Settle x as sent. y must remain claimable with the body intact — the shared
	// message row may not be dropped while a recipient still references it.
	if err := s.Sent(byRcpt["x@remote"].RecipientID); err != nil {
		t.Fatalf("sent x: %v", err)
	}
	after, err := s.Claim(t0, 10)
	if err != nil {
		t.Fatalf("claim after sent: %v", err)
	}
	if len(after) != 1 || after[0].Recipient != "y@remote" {
		t.Fatalf("after settling x, claim = %v, want only y@remote", after)
	}
	if !bytes.Equal(after[0].Body, body) {
		t.Error("body lost after a sibling recipient was settled")
	}

	// Defer y by an hour: it must drop out of the now-claim and reappear later
	// with an incremented attempt count.
	if err := s.Retry(after[0].RecipientID, t0.Add(time.Hour), "452 try later"); err != nil {
		t.Fatalf("retry y: %v", err)
	}
	if now, _ := s.Claim(t0, 10); len(now) != 0 {
		t.Errorf("a deferred recipient is still claimable now: %v", now)
	}
	later, err := s.Claim(t0.Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("claim later: %v", err)
	}
	if len(later) != 1 || later[0].Attempts != 1 {
		t.Fatalf("after retry, later claim = %v, want one item with Attempts=1", later)
	}

	// Settling the last recipient drops the message body too.
	if err := s.Sent(later[0].RecipientID); err != nil {
		t.Fatalf("sent y: %v", err)
	}
	if final, _ := s.Claim(t0.Add(2*time.Hour), 10); len(final) != 0 {
		t.Errorf("spool not empty after all recipients settled: %v", final)
	}
}

// TestSpoolDurableAcrossReopen proves the spool survives a process restart: a
// message enqueued, then the handle closed and reopened, is still claimable.
func TestSpoolDurableAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.sqlite3")
	t0 := time.Unix(2_000_000, 0)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Enqueue("a@local", []string{"x@remote"}, []byte("raw"), t0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	due, err := reopened.Claim(t0, 10)
	if err != nil {
		t.Fatalf("claim after reopen: %v", err)
	}
	if len(due) != 1 || due[0].Recipient != "x@remote" {
		t.Fatalf("after reopen, claim = %v, want the persisted x@remote", due)
	}
}
