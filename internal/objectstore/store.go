package objectstore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"hermex/internal/logging"

	_ "modernc.org/sqlite"
)

// ErrNotFound is reported when a folder or message lookup finds no such object.
var ErrNotFound = errors.New("objectstore: not found")

// ErrFolderCycle is reported when a folder copy would place a folder inside its
// own subtree, which would recurse without end.
var ErrFolderCycle = errors.New("objectstore: folder copied into its own subtree")

// ErrFolderExists is reported when a folder rename or move would collide with a
// live sibling that already holds the target name.
var ErrFolderExists = errors.New("objectstore: a folder with that name already exists")

// defaultLogger is the central activity log stamped onto every Store opened
// after a daemon installs it. Logging is a cross-cutting concern: every mailbox
// store in a process belongs to one daemon and shares its log, so a package-level
// default avoids threading a logger through Open's many call sites — a deliberate
// exception to the per-server Logger field used elsewhere. It is set once at
// daemon startup (before serving) and read-only thereafter; the atomic makes that
// publication race-clean. A nil default (the test and library baseline) disables
// store logging.
var defaultLogger atomic.Pointer[logging.Logger]

// SetDefaultLogger installs the activity log that newly opened stores report
// infrastructure failures to. A daemon calls it once at startup; passing nil
// disables store logging. Stores already open keep the logger they were stamped
// with at Open.
func SetDefaultLogger(l *logging.Logger) {
	defaultLogger.Store(l)
}

// Store is a handle to one mailbox: the MAPI object store (objdb), the IMAP/POP3
// index (idxdb), and the cid/ and eml/ content directories under a mailbox
// directory.
type Store struct {
	dir   string
	objdb *sql.DB
	idxdb *sql.DB
	// seedBuiltins controls whether a freshly created object store is
	// provisioned with the default folder hierarchy. Real mailboxes always
	// are; low-level allocator/property tests open a bare store instead.
	seedBuiltins bool
	// logger receives store infrastructure failures (SQL/IO errors); nil
	// disables logging. Stamped from defaultLogger at Open.
	logger *logging.Logger
}

// logStoreError reports a store infrastructure failure (a SQL or filesystem
// error from the underlying databases or content files) to the central log under
// the store subsystem. Logical outcomes like ErrNotFound are not infrastructure
// failures and must not be passed here. The mailbox directory identifies which
// store failed; no message content is logged.
func (s *Store) logStoreError(op string, err error) {
	s.logger.Emit(logging.Event{
		Level:     logging.LevelError,
		Subsystem: logging.Store,
		Name:      "error",
		Fields:    logging.Fields{"op": op, "mailbox": s.dir},
		Err:       err.Error(),
	})
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
// cid/ and eml/ subdirectories) if absent. A freshly created mailbox is
// provisioned with the default folder hierarchy. It fails if either database
// carries an unsupported schema version.
func Open(dir string) (*Store, error) {
	return open(dir, true)
}

// open is the shared constructor. seedBuiltins selects whether a fresh object
// store gets the default folder hierarchy; tests open a bare store to exercise
// allocators and property tables on a controlled baseline.
func open(dir string, seedBuiltins bool) (*Store, error) {
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
	s := &Store{dir: dir, objdb: objdb, idxdb: idxdb, seedBuiltins: seedBuiltins, logger: defaultLogger.Load()}
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
		if _, err := s.objdb.Exec(
			`INSERT INTO configurations (config_id, config_value) VALUES (?, ?)`,
			cfgSchemaVersion, objectSchemaVersion); err != nil {
			return err
		}
		guid, err := s.seedStore()
		if err != nil {
			return err
		}
		if s.seedBuiltins {
			return s.seedMailbox(guid)
		}
		return nil
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
