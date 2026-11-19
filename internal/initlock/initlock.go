// Package initlock serializes the first open of a SQLite database across
// processes.
//
// Several openers of a file that does not exist yet race twice: switching the new
// file to WAL needs an exclusive lock that SQLite takes without its busy handler,
// so the losers fail with SQLITE_BUSY at once, and an opener can read a schema
// another one has only half created. Holding this lock across the first
// connection and the schema run makes every opener after the first find a
// finished file. The kernel releases the lock when a holder dies, so a crash
// cannot wedge the next start.
package initlock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Take blocks until it holds the exclusive initialization lock named by path and
// returns the function that releases it.
func Take(path string) (release func() error, err error) {
	// #nosec G304 -- callers pass a fixed name beside a database they own
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("initlock: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, errors.Join(fmt.Errorf("initlock: lock %s: %w", path, err), f.Close())
	}
	return f.Close, nil
}
