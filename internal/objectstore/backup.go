package objectstore

import (
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
// The eml/ cache is deliberately not copied. It is a second copy of every message
// that the store re-synthesizes on the next read, so carrying it would roughly
// double the size of a backup that is no more complete for it.
func (s *Store) Backup(dest string) error {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	for _, db := range []struct {
		handle *sql.DB
		name   string
	}{
		{s.objdb, "objects.sqlite3"},
		{s.idxdb, "imapindex.sqlite3"},
	} {
		out := filepath.Join(dest, db.name)
		// VACUUM INTO refuses to overwrite, so a re-run into the same directory
		// replaces rather than fails: the previous copy is superseded, not merged.
		if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
			return err
		}
		if _, err := db.handle.Exec(`VACUUM INTO ?`, out); err != nil {
			return fmt.Errorf("objectstore: snapshot %s: %w", db.name, err)
		}
	}
	return copyTree(filepath.Join(s.dir, "cid"), filepath.Join(dest, "cid"))
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
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
