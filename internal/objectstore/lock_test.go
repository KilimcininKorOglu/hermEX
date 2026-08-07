package objectstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestSweepRefusesWhileTheMailboxIsOpen is the point of the lock. The sweep deletes
// content files a live writer may be about to reference: a write that dedup-reuses
// an existing file attaches a reference without creating anything on disk, so a file
// can go from unreferenced to referenced between the sweep's snapshot and its
// reference scan and still be deleted. The precondition was documented on the API
// and repeated in the CLI usage, and enforced in neither.
func TestSweepRefusesWhileTheMailboxIsOpen(t *testing.T) {
	first := openSeededStore(t)

	second, err := Open(first.Dir())
	if err != nil {
		t.Fatalf("a second open of a live mailbox failed: %v", err)
	}
	defer second.Close()

	if _, err := second.SweepOrphanContent(); !errors.Is(err, ErrMailboxBusy) {
		t.Errorf("sweep with the mailbox open elsewhere = %v, want ErrMailboxBusy", err)
	}
}

// TestSweepRunsOnAnIdleMailbox guards the other direction: enforcing the
// precondition must not make the maintenance command unusable on a mailbox that
// really is idle, which is the only state it was ever meant to run in.
func TestSweepRunsOnAnIdleMailbox(t *testing.T) {
	s := openSeededStore(t)

	// An orphan: a content file no property references.
	orphan := filepath.Join(s.Dir(), "cid", "S-ab")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(orphan, "cdef.zst")
	if err := os.WriteFile(path, []byte("not referenced by anything"), 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := s.SweepOrphanContent()
	if err != nil {
		t.Fatalf("sweep on an idle mailbox refused: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed %d files, want the 1 orphan", removed)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the orphan survived the sweep")
	}
}

// TestMailboxIsUsableAfterASweep proves the pass hands the mailbox back. It holds
// the lock exclusively while it runs, so a bug that left it exclusive would make
// every later open of that mailbox wait out its timeout and then fail.
func TestMailboxIsUsableAfterASweep(t *testing.T) {
	s := openSeededStore(t)
	if _, err := s.SweepOrphanContent(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	other, err := Open(s.Dir())
	if err != nil {
		t.Fatalf("the mailbox cannot be opened after a sweep: %v", err)
	}
	defer other.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("opening after a sweep took %s, so the pass left the lock held", elapsed)
	}
	// And it still works, not just opens.
	if _, err := other.AppendMessage(mapi.PrivateFIDInbox,
		[]byte("Subject: after\r\n\r\nbody\r\n"), time.Unix(1700000000, 0), 0); err != nil {
		t.Errorf("the mailbox does not accept a write after a sweep: %v", err)
	}
}

// TestClosingReleasesTheMailbox proves the in-use mark is tied to the store's life.
// A daemon that opens and closes a mailbox must not leave it looking busy, or the
// maintenance command becomes permanently unusable on a system that is idle.
func TestClosingReleasesTheMailbox(t *testing.T) {
	s := openSeededStore(t)
	dir := s.Dir()

	holder, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SweepOrphanContent(); !errors.Is(err, ErrMailboxBusy) {
		t.Fatalf("sweep = %v, want ErrMailboxBusy while a second store is open", err)
	}
	holder.Close()

	if _, err := s.SweepOrphanContent(); err != nil {
		t.Errorf("sweep after the other store closed = %v, want it to run", err)
	}
}

// TestConcurrentOpensDoNotBlockEachOther proves the in-use lock is shared. It is
// taken on every mailbox open, including the per-request opens the web API makes,
// so an exclusive one would serialize the whole mail path onto one mailbox at a
// time.
func TestConcurrentOpensDoNotBlockEachOther(t *testing.T) {
	s := openSeededStore(t)
	dir := s.Dir()

	start := time.Now()
	stores := make([]*Store, 0, 8)
	for range 8 {
		st, err := Open(dir)
		if err != nil {
			t.Fatalf("a concurrent open was refused: %v", err)
		}
		stores = append(stores, st)
	}
	elapsed := time.Since(start)
	for _, st := range stores {
		st.Close()
	}
	if elapsed > openTimeout {
		t.Errorf("8 opens took %s, so they are contending on the lock", elapsed)
	}
}
