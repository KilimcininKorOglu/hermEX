// Package atomicfile replaces files so a reader, or a restart after a crash or a
// power loss, only ever finds the old content or the new, never a partial file:
// the content goes to a temporary file in the target's directory, is synced to
// disk, renamed over the target, and the directory is synced so the rename itself
// survives a power loss.
package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// TempMarker is part of every temporary file name, ".<target>.tmp-<random>", so a
// scan of a directory can tell a crash leftover from real content.
const TempMarker = ".tmp-"

// The durability steps, replaceable only by this package's tests so each one can
// be failed on purpose and its order observed.
var (
	syncFile   = func(f *os.File) error { return f.Sync() }
	renameFile = os.Rename
	syncDir    = syncDirectory
)

// WriteFile atomically replaces path with data, created with perm.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return WriteFunc(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// WriteFunc atomically replaces path with what write produces, created with perm.
// A write error leaves path as it was and removes the temporary file.
func WriteFunc(path string, perm os.FileMode, write func(io.Writer) error) (err error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, "."+base+TempMarker+"*")
	if err != nil {
		return fmt.Errorf("atomicfile: create temp for %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		return errors.Join(fmt.Errorf("atomicfile: chmod %s: %w", path, err), tmp.Close())
	}
	if err := write(tmp); err != nil {
		return errors.Join(fmt.Errorf("atomicfile: write %s: %w", path, err), tmp.Close())
	}
	if err := syncFile(tmp); err != nil {
		return errors.Join(fmt.Errorf("atomicfile: sync %s: %w", path, err), tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomicfile: close %s: %w", path, err)
	}
	return commit(name, path)
}

// SyncRename durably moves a file this package did not write, such as a database
// another library built, over path: it syncs src, renames it, and syncs the
// directory. src must be on the same file system as path.
func SyncRename(src, path string) error {
	// #nosec G304 -- src is a file the caller itself produced
	f, err := os.OpenFile(src, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("atomicfile: open %s: %w", src, err)
	}
	if err := syncFile(f); err != nil {
		return errors.Join(fmt.Errorf("atomicfile: sync %s: %w", src, err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("atomicfile: close %s: %w", src, err)
	}
	return commit(src, path)
}

// IsTemp reports whether a file name is one of this package's temporary files.
func IsTemp(name string) bool {
	return strings.HasPrefix(name, ".") && strings.Contains(name, TempMarker)
}

// commit renames a synced file over path and syncs the directory.
func commit(src, path string) error {
	if err := renameFile(src, path); err != nil {
		return fmt.Errorf("atomicfile: rename onto %s: %w", path, err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("atomicfile: sync directory of %s: %w", path, err)
	}
	return nil
}

// syncDirectory syncs a directory so a rename inside it survives a power loss.
func syncDirectory(dir string) error {
	// #nosec G304 -- dir is the directory of a path the caller chose to write
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
