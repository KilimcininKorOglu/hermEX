package objectstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrMailboxBusy is returned when a maintenance pass cannot run because the
// mailbox is open elsewhere. It names the precondition the pass could previously
// only document.
var ErrMailboxBusy = errors.New("objectstore: the mailbox is open elsewhere")

// lockName is the file whose advisory lock stands for "this mailbox is in use". It
// carries no content; only the lock on it matters. An OS advisory lock is the right
// primitive here because the holders are separate processes (a daemon and the
// maintenance command) and because the kernel releases it if a holder dies, so a
// crash cannot leave a mailbox permanently locked.
const lockName = ".mailbox.lock"

// openTimeout bounds how long opening a mailbox waits for a maintenance pass to
// finish. A pass is short, and a caller that would rather fail than wait gets a
// clear error instead of an unbounded stall on a mail path.
const openTimeout = 3 * time.Second

// lockShared takes the in-use lock for a store being opened. It is shared, so any
// number of readers and writers hold it at once and only a maintenance pass is
// excluded. It retries briefly rather than failing the instant a pass holds the
// mailbox, since a pass finishes on its own.
func lockShared(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(openTimeout)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, fmt.Errorf("objectstore: %s is being maintained: %w", dir, err)
			}
			return nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// withExclusiveLock runs fn while holding the mailbox exclusively, and reports
// ErrMailboxBusy without running it when anything else holds the mailbox open.
//
// This is what makes a maintenance pass's precondition real. The orphan-content
// sweep deletes files a live writer may be about to reference: a write that
// dedup-reuses an existing file attaches a reference without creating anything on
// disk, so between the sweep's snapshot and its reference scan a file can go from
// unreferenced to referenced and still be deleted. Documenting "run with the
// mailbox idle" left an operator with no way to satisfy it and no way to know they
// had not.
//
// The store's own shared lock is dropped first: the caller holds the mailbox open
// to run the pass at all, so upgrading a lock it already holds would otherwise
// deadlock against itself. Nothing else uses the store while fn runs.
func (s *Store) withExclusiveLock(fn func() error) error {
	if s.lock == nil {
		return fn() // no lock was taken for this store; nothing to coordinate with
	}
	fd := int(s.lock.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("%w: %s", ErrMailboxBusy, s.dir)
		}
		return err
	}
	// Back to shared afterwards, so the store stays usable and a later pass can
	// still upgrade.
	defer syscall.Flock(fd, syscall.LOCK_SH)
	return fn()
}
