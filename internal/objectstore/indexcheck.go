package objectstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"hermex/internal/logging"
	"hermex/internal/migrate"
)

// indexTables are the IMAP index's own tables. The count found in sqlite_master
// is what separates a fresh index (none of them yet) from a damaged one (some of
// them lost).
var indexTables = []string{"folders", "messages", "mapping"}

// indexProbes read from each index table. A table can be present and still
// unreadable, so the schema listing alone does not prove the index usable. Each
// probe stops at the first row, so it costs one index seek.
var indexProbes = []string{
	`SELECT folder_id FROM folders LIMIT 1`,
	`SELECT message_id FROM messages LIMIT 1`,
	`SELECT message_id FROM mapping LIMIT 1`,
}

// permanentDBFailure reports whether a SQLite failure condemns the database.
// Rebuilding discards the whole IMAP index of a mailbox, so only a failure that
// will not resolve on its own qualifies: a lock, an I/O error or an out-of-memory
// condition may work on the next run and must never discard an intact index.
func permanentDBFailure(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() & 0xff { // mask out the extended result codes
	case sqlite3.SQLITE_ERROR, // a missing table reports this
		sqlite3.SQLITE_CORRUPT,
		sqlite3.SQLITE_NOTADB,
		sqlite3.SQLITE_FORMAT:
		return true
	}
	return false
}

// indexTableCount returns how many of the index's own tables the schema listing
// holds.
func (s *Store) indexTableCount() (int, error) {
	var n int
	err := s.idxdb.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN (?, ?, ?)`,
		indexTables[0], indexTables[1], indexTables[2]).Scan(&n)
	return n, err
}

// indexDamage returns the failure that condemns the index, or nil when the index
// reads or the failure may resolve on its own.
func (s *Store) indexDamage() error {
	for _, probe := range indexProbes {
		err := s.probeIndex(probe)
		if err == nil {
			continue
		}
		if !permanentDBFailure(err) {
			s.logIndexUnverified(err)
			return nil
		}
		return err
	}
	return nil
}

// probeIndex runs one probe and reports the failure it produced.
func (s *Store) probeIndex(query string) error {
	rows, err := s.idxdb.Query(query)
	if err != nil {
		return err
	}
	err = rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return err
}

// createIndexBaseline builds a fresh IMAP index: the baseline schema and its
// stamped version.
func (s *Store) createIndexBaseline() error {
	for _, stmt := range indexBaseline {
		if _, err := s.idxdb.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
		}
	}
	// PRAGMA does not accept bound parameters; the value is a trusted constant.
	_, err := s.idxdb.Exec(fmt.Sprintf("PRAGMA user_version=%d", indexSchemaVersion))
	return err
}

// runIndexMigrations carries the index forward past its baseline and refuses one
// recorded newer than this binary.
func (s *Store) runIndexMigrations() error {
	return migrate.Run(context.Background(),
		&migrate.SQLiteDriver{DB: s.idxdb, Ver: migrate.UserVersion()}, indexSchemaVersion, indexMigrations)
}

// rebuildIndex discards a damaged IMAP index and builds it again from the object
// store, which holds every message the index projects. Without this the mailbox
// breaks on every run for good: the object store is intact, so nothing is lost,
// yet IMAP and POP3 either fail on each call or serve an empty mailbox, and no
// pass ever repairs it.
//
// The rebuilt index assigns new UIDs, so every IMAP client resyncs the mailbox.
// That is the price of the rebuild and the reason a transient failure must never
// reach here.
//
// It takes the mailbox exclusively, so two daemons opening a damaged mailbox at
// the same time cannot delete the file under each other. A mailbox held open
// elsewhere reports ErrMailboxBusy rather than rebuilding.
func (s *Store) rebuildIndex(cause error) error {
	if err := s.withExclusiveLock(func() error {
		if err := s.idxdb.Close(); err != nil {
			return err
		}
		if err := deleteIndexFiles(s.dir); err != nil {
			return err
		}
		db, err := sql.Open("sqlite", dsn(filepath.Join(s.dir, indexDBName)))
		if err != nil {
			return err
		}
		s.idxdb = db
		if err := s.createIndexBaseline(); err != nil {
			return err
		}
		if err := s.runIndexMigrations(); err != nil {
			return err
		}
		return s.reindexMailFolders()
	}); err != nil {
		s.logIndexRebuildFailed(cause, err)
		return err
	}
	s.logIndexRebuild(cause)
	return nil
}

// deleteIndexFiles removes the index database and its journal companions, so the
// rebuild starts from no file at all rather than from a damaged one.
func deleteIndexFiles(dir string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(dir, indexDBName+suffix)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// adoptExistingMail fills a newly created index from the object store. A mailbox
// being provisioned has nothing to adopt and returns at once; an index file that
// was lost outright is indistinguishable from a fresh one on disk, and without
// this the mailbox opens clean and serves no mail at all, silently and for good.
func (s *Store) adoptExistingMail() error {
	folders, err := s.mailFolderIDs()
	if err != nil {
		return err
	}
	n, err := s.mailMessageCount(folders)
	if err != nil || n == 0 {
		return err
	}
	cause := fmt.Errorf("the index file was absent while the object store held %d messages", n)
	if err := s.withExclusiveLock(s.reindexMailFolders); err != nil {
		s.logIndexRebuildFailed(cause, err)
		return err
	}
	s.logIndexRebuild(cause)
	return nil
}

// mailMessageCount counts the object-store messages the given folders hold.
func (s *Store) mailMessageCount(folders []int64) (int, error) {
	total := 0
	for _, id := range folders {
		var n int
		if err := s.objdb.QueryRow(
			`SELECT COUNT(*) FROM messages WHERE parent_fid=? AND is_deleted=0`, id).Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// reindexMailFolders projects every mail folder of the object store back into the
// IMAP index.
func (s *Store) reindexMailFolders() error {
	folders, err := s.mailFolderIDs()
	if err != nil {
		return err
	}
	for _, id := range folders {
		if err := s.ReindexFolder(id); err != nil {
			return err
		}
	}
	return nil
}

// logIndexRebuildFailed records a rebuild that was needed and did not run, which
// is a separate outcome from a rebuild that did. Reporting it as a rebuild would
// tell the operator the mailbox was repaired when it was not. A mailbox held open
// elsewhere reports ErrMailboxBusy here, and the next open retries.
func (s *Store) logIndexRebuildFailed(cause, failure error) {
	s.logger.Emit(logging.Event{
		Level:     logging.LevelWarn,
		Subsystem: logging.Store,
		Name:      "mailbox.index_rebuild_failed",
		Fields: logging.Fields{
			"mailbox": s.dir,
			"cause":   cause.Error(),
			"failure": failure.Error(),
		},
	})
}

// logIndexRebuild records that a mailbox lost its IMAP index and that the rebuild
// finished. Every client resyncs after this, so the operator needs the line to
// tell a rebuild from a client-side fault, and the cause names which failure
// condemned the index.
func (s *Store) logIndexRebuild(cause error) {
	s.logger.Emit(logging.Event{
		Level:     logging.LevelWarn,
		Subsystem: logging.Store,
		Name:      "mailbox.index_rebuilt",
		Fields: logging.Fields{
			"mailbox": s.dir,
			"cause":   cause.Error(),
		},
	})
}

// logIndexUnverified records that the index check could not run. The index is
// kept, so the only evidence otherwise is its absence.
func (s *Store) logIndexUnverified(cause error) {
	s.logger.Emit(logging.Event{
		Level:     logging.LevelWarn,
		Subsystem: logging.Store,
		Name:      "mailbox.index_unverified",
		Fields: logging.Fields{
			"mailbox": s.dir,
			"cause":   cause.Error(),
		},
	})
}
