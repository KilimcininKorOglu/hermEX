package relay

import (
	"context"
	"errors"
	"io"
	"net"
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

// closeT closes c and fails the test when the close fails. A closed network
// listener or connection already gone is the expected end of a fake server and
// passes.
func closeT(t *testing.T, c io.Closer, what string) {
	t.Helper()
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("close %s: %v", what, err)
	}
}

// serveUntilCleanup runs serve on ln until the test ends, then closes ln and
// fails the test unless serve stopped because its listener closed.
func serveUntilCleanup(t *testing.T, ln net.Listener, serve func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- serve() }()
	t.Cleanup(func() {
		closeT(t, ln, "listener")
		if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("serve: %v", err)
		}
	})
}

// replyWriter writes a fake server's replies and keeps the first write error,
// so the session stops at a failed write instead of dropping it.
type replyWriter struct {
	conn net.Conn
	err  error
}

// send writes s unless an earlier write already failed.
func (w *replyWriter) send(s string) {
	if w.err == nil {
		_, w.err = w.conn.Write([]byte(s))
	}
}

// mustCount runs a single-value COUNT-style query on the spool and returns it.
func mustCount(t *testing.T, s *Spool, query string, args ...any) int {
	t.Helper()
	var n int
	err := s.db.QueryRow(query, args...).Scan(&n)
	mustNoErr(t, err, "query "+query)
	return n
}
