package relay

import (
	"context"
	"testing"
	"time"
)

// This file is the package's assertion vocabulary. A test states one fact per call,
// so a failure names the fact that broke rather than the condition that evaluated.

// wantEq fails the test unless the value equals what the caller expected.
func wantEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// wantTrue fails the test unless the condition holds.
func wantTrue(t *testing.T, got bool, what string) {
	t.Helper()
	if !got {
		t.Errorf("%s: false, want true", what)
	}
}

// wantFalse fails the test when the condition holds.
func wantFalse(t *testing.T, got bool, what string) {
	t.Helper()
	if got {
		t.Errorf("%s: true, want false", what)
	}
}

// mustNoErr stops the test on an error from a step under test.
func mustNoErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// mustClaim claims the due recipients and fails the test when the claim fails,
// so a broken spool is never read as an empty queue.
func mustClaim(t *testing.T, s *Spool, now time.Time, limit int) []Item {
	t.Helper()
	items, err := s.Claim(now, limit)
	mustNoErr(t, err, "claim the due recipients")
	return items
}

// mustProcess runs one worker pass and fails the test when the pass errors.
func mustProcess(t *testing.T, w *Worker, now time.Time) int {
	t.Helper()
	sent, err := w.ProcessDue(context.Background(), now)
	mustNoErr(t, err, "run a worker pass")
	return sent
}

// mustCount runs a single-value COUNT-style query on the spool and returns it.
func mustCount(t *testing.T, s *Spool, query string, args ...any) int {
	t.Helper()
	var n int
	err := s.db.QueryRow(query, args...).Scan(&n)
	mustNoErr(t, err, "query "+query)
	return n
}
