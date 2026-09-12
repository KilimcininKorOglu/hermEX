package objectstore

import (
	"database/sql"
	"testing"
)

// TestStoreBoundsTheWriteAheadLog pins the bound on both of a mailbox's
// databases. SQLite never shrinks a write-ahead log on its own, so without the
// bound one oversized transaction leaves its size on disk for as long as a
// connection stays open, and a cached store stays open for the process's life.
func TestStoreBoundsTheWriteAheadLog(t *testing.T) {
	s, err := Open(t.TempDir())
	mustNoErr(t, "open", err)
	defer s.Close()

	for _, db := range []struct {
		name string
		db   *sql.DB
	}{{objectsDBName, s.objdb}, {indexDBName, s.idxdb}} {
		var limit int64
		mustNoErr(t, "read "+db.name+" journal_size_limit",
			db.db.QueryRow(`PRAGMA journal_size_limit`).Scan(&limit))
		if limit != journalSizeLimit {
			t.Errorf("%s journal_size_limit = %d, want %d", db.name, limit, journalSizeLimit)
		}
	}
}
