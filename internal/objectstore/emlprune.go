package objectstore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PruneEMLCache drops cached wire-form files last written before cutoff,
// returning how many were removed and how many bytes that reclaimed.
//
// The eml file is a cache, not the canonical form: GetMessageRaw re-synthesizes
// a missing one from the stored object and rewrites both the file and the
// recorded RFC822 size, so removing any of them is always safe. What it is not
// is free, the cache holds a second copy of every live message and roughly
// doubles the space a mailbox occupies. Nothing evicts it on its own, and this
// is the lever that lets an operator reclaim that space without deleting mail.
//
// Cutoff is compared against the file's modification time, which is when the
// wire form was last written rather than when it was last read: the point is to
// spare recent mail, which is what clients actually fetch, and reclaim the long
// tail nobody opens. The cost of pruning a file that is then read is one
// re-export on the next read.
//
// A file created or rewritten during the pass is never a candidate, and the
// in-flight temporaries writeEML leaves behind are skipped, so this is safe to
// run against a live mailbox.
func (s *Store) PruneEMLCache(cutoff time.Time) (removed int, reclaimed int64, err error) {
	root := filepath.Join(s.dir, "eml")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		// A dot file is an in-flight writeEML temporary; a directory is not ours.
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue // removed underneath us; nothing left to reclaim
		}
		if err != nil {
			return removed, reclaimed, err
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(root, e.Name())); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return removed, reclaimed, err
		}
		removed++
		reclaimed += info.Size()
	}
	return removed, reclaimed, nil
}
