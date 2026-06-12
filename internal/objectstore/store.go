package objectstore

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Store is a handle to one mailbox: the MAPI object store (objdb), the IMAP/POP3
// index (idxdb), and the cid/ and eml/ content directories under a mailbox
// directory.
type Store struct {
	dir   string
	objdb *sql.DB
	idxdb *sql.DB
}

// dsn builds the modernc.org/sqlite connection string with the pragmas applied
// on every pooled connection: a busy timeout, WAL journaling, enforced foreign
// keys, and FULL synchronous mode for durability.
func dsn(path string) string {
	return "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(FULL)"
}

// Open opens the mailbox rooted at dir, creating and initializing it (and its
// cid/ and eml/ subdirectories) if absent. It fails if either database carries
// an unsupported schema version.
func Open(dir string) (*Store, error) {
	for _, sub := range []string{"", "cid", "eml"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	objdb, err := sql.Open("sqlite", dsn(filepath.Join(dir, "objects.sqlite3")))
	if err != nil {
		return nil, err
	}
	idxdb, err := sql.Open("sqlite", dsn(filepath.Join(dir, "imapindex.sqlite3")))
	if err != nil {
		objdb.Close()
		return nil, err
	}
	s := &Store{dir: dir, objdb: objdb, idxdb: idxdb}
	if err := s.ensureSchema(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Close releases both database handles.
func (s *Store) Close() error {
	err1 := s.objdb.Close()
	err2 := s.idxdb.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// ensureSchema creates the schema on a fresh mailbox and rejects an unknown one.
func (s *Store) ensureSchema() error {
	if err := s.ensureObjectSchema(); err != nil {
		return fmt.Errorf("objectstore: object schema: %w", err)
	}
	if err := s.ensureIndexSchema(); err != nil {
		return fmt.Errorf("objectstore: index schema: %w", err)
	}
	return nil
}

func (s *Store) ensureObjectSchema() error {
	var name string
	err := s.objdb.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='configurations'`).Scan(&name)
	if err == sql.ErrNoRows {
		for _, stmt := range objectDDL {
			if _, err := s.objdb.Exec(stmt); err != nil {
				return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
			}
		}
		_, err := s.objdb.Exec(
			`INSERT INTO configurations (config_id, config_value) VALUES (?, ?)`,
			cfgSchemaVersion, objectSchemaVersion)
		return err
	}
	if err != nil {
		return err
	}
	var v int
	if err := s.objdb.QueryRow(
		`SELECT config_value FROM configurations WHERE config_id=?`, cfgSchemaVersion).Scan(&v); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if v != objectSchemaVersion {
		return fmt.Errorf("schema version %d unsupported (want %d)", v, objectSchemaVersion)
	}
	return nil
}

func (s *Store) ensureIndexSchema() error {
	var name string
	err := s.idxdb.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='folders'`).Scan(&name)
	if err == sql.ErrNoRows {
		for _, stmt := range indexDDL {
			if _, err := s.idxdb.Exec(stmt); err != nil {
				return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
			}
		}
		// PRAGMA does not accept bound parameters; the value is a trusted constant.
		_, err := s.idxdb.Exec(fmt.Sprintf("PRAGMA user_version=%d", indexSchemaVersion))
		return err
	}
	if err != nil {
		return err
	}
	var v int
	if err := s.idxdb.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v != indexSchemaVersion {
		return fmt.Errorf("schema version %d unsupported (want %d)", v, indexSchemaVersion)
	}
	return nil
}

// firstLine returns the first non-blank line of a SQL statement for error
// context.
func firstLine(stmt string) string {
	for line := range strings.SplitSeq(stmt, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return stmt
}
