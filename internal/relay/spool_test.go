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

// TestSpoolListRetryDelete proves the administrative mail-queue projection and
// actions: List reports every queued recipient with its message metadata, RetryNow
// makes a deferred recipient immediately claimable without losing its history, and
// Delete drops a recipient (and the body once none remain) without a bounce.
func TestSpoolListRetryDelete(t *testing.T) {
	s := openSpool(t)
	t0 := time.Unix(2_000_000, 0)
	body := []byte("From: a@local\r\nSubject: hi\r\n\r\nbody\r\n")
	if err := s.Enqueue("a@local", []string{"x@remote", "y@remote"}, body, t0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Defer x far into the future with a recorded error (a transient failure).
	due, _ := s.Claim(t0, 10)
	var xID, yID int64
	for _, it := range due {
		if it.Recipient == "x@remote" {
			xID = it.RecipientID
		} else {
			yID = it.RecipientID
		}
	}
	future := t0.Add(time.Hour)
	if err := s.Retry(xID, future, "451 greylisted"); err != nil {
		t.Fatalf("retry: %v", err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d entries, want 2", len(list))
	}
	var x QueueEntry
	for _, e := range list {
		if e.RecipientID == xID {
			x = e
		}
	}
	if x.From != "a@local" || x.Recipient != "x@remote" {
		t.Errorf("entry envelope wrong: from=%q rcpt=%q", x.From, x.Recipient)
	}
	if x.Attempts != 1 || x.LastError != "451 greylisted" {
		t.Errorf("entry history wrong: attempts=%d err=%q", x.Attempts, x.LastError)
	}
	if x.Size != len(body) {
		t.Errorf("entry size = %d, want %d", x.Size, len(body))
	}
	if !x.NextAttempt.Equal(future.UTC()) {
		t.Errorf("entry next-attempt = %v, want %v", x.NextAttempt, future.UTC())
	}

	// x is not yet due; RetryNow makes it claimable immediately, keeping its history.
	if got, _ := s.Claim(t0, 10); len(got) != 1 || got[0].RecipientID != yID {
		t.Fatalf("before flush only y is due, got %d items", len(got))
	}
	if err := s.RetryNow(xID, t0); err != nil {
		t.Fatalf("retry-now: %v", err)
	}
	got, _ := s.Claim(t0, 10)
	if len(got) != 2 {
		t.Fatalf("after flush both are due, got %d", len(got))
	}
	for _, it := range got {
		if it.RecipientID == xID && it.Attempts != 1 {
			t.Errorf("flush reset the attempt count to %d, want 1 (history kept)", it.Attempts)
		}
	}

	// Delete x: it vanishes, the body survives for y, then deleting y drops the body.
	if err := s.Delete(xID); err != nil {
		t.Fatalf("delete x: %v", err)
	}
	if list, _ := s.List(); len(list) != 1 || list[0].RecipientID != yID {
		t.Fatalf("after deleting x, List should hold only y")
	}
	if err := s.Delete(yID); err != nil {
		t.Fatalf("delete y: %v", err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("after deleting all recipients the queue should be empty, got %d", len(list))
	}
	// A second delete of a gone recipient is a no-op, not an error.
	if err := s.Delete(xID); err != nil {
		t.Errorf("deleting an already-gone recipient should be a no-op, got %v", err)
	}
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
