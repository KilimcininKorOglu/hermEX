package objectstore

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"hermex/internal/atomicfile"
)

// Backup writes a self-contained copy of this mailbox into dest, which is created
// if it does not exist. The copy is an ordinary mailbox directory: opening it with
// Open yields the same messages, so a restore is a plain file copy back into place
// with the services stopped.
//
// It is safe to run against a live mailbox. The two SQLite files are copied with
// VACUUM INTO, which snapshots inside a read transaction, so a writer mid
// transaction cannot produce a torn copy the way a file-level copy of a database
// with a pending write-ahead log would.
//
// Order matters: the databases are snapshotted first and the content files second.
// A content file is written before the row that references it commits, so
// everything the snapshot points at is already on disk by the time the copy walks
// cid/. Doing it the other way round could miss a file the snapshot references.
//
// The two databases are snapshotted in reverse write order: the IMAP index first,
// the objects second. Each VACUUM INTO is its own read transaction, so against a
// live writer the two snapshots are taken at different instants with no cross-file
// point-in-time. A live append commits the object first and the index row last, so
// snapshotting the index first guarantees that every index row in the copy points at
// an object already committed before that snapshot, hence present in the later
// objects snapshot. The only residual skew is the harmless direction: an object with
// no index row yet (invisible to IMAP, reclaimed by the orphan-content sweep), never
// an index row whose message body is missing.
//
// The eml/ cache is deliberately not copied. It is a second copy of every message
// that the store re-synthesizes on the next read, so carrying it would roughly
// double the size of a backup that is no more complete for it.
func (s *Store) Backup(dest string) error {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	for i, db := range []struct {
		handle *sql.DB
		name   string
	}{
		{s.idxdb, "imapindex.sqlite3"},
		{s.objdb, "objects.sqlite3"},
	} {
		if err := vacuumInto(db.handle, filepath.Join(dest, db.name)); err != nil {
			return fmt.Errorf("objectstore: snapshot %s: %w", db.name, err)
		}
		// Test-only seam: fire a concurrent write into the between-snapshots window
		// so a test can prove the copy is still consistent. Nil in production.
		if i == 0 && s.backupHook != nil {
			s.backupHook()
		}
	}
	return copyTree(filepath.Join(s.dir, "cid"), filepath.Join(dest, "cid"))
}

// vacuumInto snapshots a database to out. VACUUM INTO writes its target in
// place and refuses an existing one, so the snapshot goes to a fresh name beside
// out and is moved over it durably: an interrupted run leaves the previous copy,
// never a torn one under the real name.
func vacuumInto(db *sql.DB, out string) error {
	tmp := filepath.Join(filepath.Dir(out), "."+filepath.Base(out)+atomicfile.TempMarker+"vacuum")
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := db.Exec(`VACUUM INTO ?`, tmp); err != nil {
		return errors.Join(err, removeIfPresent(tmp))
	}
	return atomicfile.SyncRename(tmp, out)
}

// removeIfPresent deletes path, treating an absent file as done.
func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// copyTree copies a directory recursively. A missing source is not an error: a
// mailbox that has never stored offloaded content has no cid/ tree.
func copyTree(src, dst string) error {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

// copyFile copies one regular file, creating the destination fresh.
func copyFile(src, dst string) (err error) {
	// #nosec G304 -- both paths are inside the operator's data directory and the backup root
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer closeInto(in, &err)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return atomicfile.WriteFunc(dst, 0o600, func(out io.Writer) error {
		_, err := io.Copy(out, in)
		return err
	})
}
