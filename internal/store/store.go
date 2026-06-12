package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Store is a handle to one mailbox database.
type Store struct {
	db *sql.DB
}

// dsn builds the modernc.org/sqlite connection string with the pragmas applied
// on every pooled connection: a busy timeout (to ride out concurrent writers),
// WAL journaling, enforced foreign keys, and NORMAL synchronous mode.
func dsn(path string) string {
	return "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
}

// Open opens the mailbox database at path, creating and initializing it if it
// does not yet exist. It fails if the existing schema version is not understood.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.ensureSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// ensureSchema applies the DDL (idempotently) and reconciles the schema
// version, writing it for a fresh store and rejecting an unknown one.
func (s *Store) ensureSchema() error {
	for _, stmt := range schemaDDL {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("store: apply schema: %w", err)
		}
	}
	var v int
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v)
	switch {
	case err == sql.ErrNoRows:
		_, err = s.db.Exec(`INSERT INTO meta (key, value) VALUES ('schema_version', ?)`, schemaVersion)
		return err
	case err != nil:
		return fmt.Errorf("store: read schema version: %w", err)
	case v != schemaVersion:
		return fmt.Errorf("store: schema version %d is not supported (want %d)", v, schemaVersion)
	}
	return nil
}
