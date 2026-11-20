package initlock

import (
	"path/filepath"
	"testing"
	"time"
)

// TestTakeWaitsForTheHolder proves the lock is exclusive: a second Take on the
// same path returns only after the first holder releases it. flock locks are per
// open file description, so two Takes in one process contend like two processes.
func TestTakeWaitsForTheHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.init-lock")
	release, err := Take(path)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan func() error, 1)
	go func() {
		r, err := Take(path)
		if err != nil {
			t.Error(err)
			close(got)
			return
		}
		got <- r
	}()
	select {
	case <-got:
		t.Fatal("a second Take returned while the first still held the lock")
	case <-time.After(200 * time.Millisecond):
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	select {
	case r, ok := <-got:
		if ok {
			_ = r()
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second Take still waits after the release")
	}
}

// TestTakeReportsAnUnopenablePath proves a lock file that cannot be created is an
// error, not a lock silently skipped.
func TestTakeReportsAnUnopenablePath(t *testing.T) {
	if _, err := Take(filepath.Join(t.TempDir(), "missing", "db.init-lock")); err == nil {
		t.Fatal("Take in a missing directory returned no error")
	}
}
