package objectstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/migrate"

	_ "modernc.org/sqlite"
)

// ErrNotFound is reported when a folder or message lookup finds no such object.
var ErrNotFound = errors.New("objectstore: not found")

// ErrNotProvisioned is reported by OpenPublicExisting when a domain has no public
// store yet. Read surfaces treat it as "this domain has no public folders" rather
// than provisioning one as a side effect of a read.
var ErrNotProvisioned = errors.New("objectstore: public store not provisioned")

// ErrFolderCycle is reported when a folder copy would place a folder inside its
// own subtree, which would recurse without end.
var ErrFolderCycle = errors.New("objectstore: folder copied into its own subtree")

// ErrFolderExists is reported when a folder rename or move would collide with a
// live sibling that already holds the target name.
var ErrFolderExists = errors.New("objectstore: a folder with that name already exists")

// ErrObjectDeleted is reported when an ICS move-import names a source message the
// store no longer holds. Callers map it to SYNC_E_OBJECT_DELETED.
var ErrObjectDeleted = errors.New("objectstore: object deleted")

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

// ChangeEvent describes one committed mailbox mutation, published to the central
// notify relay so a long-poll consumer (MAPI/HTTP NotificationWait, EAS Ping, EWS
// streaming, IMAP IDLE) wakes and runs its own authoritative diff the instant the
// change lands instead of on its next poll tick. CN/Op/Mid are enrichment — the
// consumer's diff is what observes the change — so they need only be best-effort
// accurate; a delete that bumps no change number still wakes the consumer, whose
// diff sees the vanished row regardless.
type ChangeEvent struct {
	MailboxDir string // the mutated store's directory (Dir()) — the key a consumer matches against the mailbox it is polling
	Op         string // the mutation kind: create | modify | flags | delete | folder
	CN         uint64 // the change number stamped on the write; 0 when the write bumps none (a delete, or an index-only change)
	Mid        string // the mid_string of a deleted/created message, when cheaply in scope; empty otherwise
}

// changePublisher is the central push hook a daemon installs at startup so a
// committed mailbox mutation wakes the long-poll consumers in real time. It mirrors
// defaultLogger: a package-level atomic so Open's many write paths need no threaded
// publisher, set once before serving and read-only after. A nil hook (the test and
// library baseline) is byte-identical to the pre-push behaviour — publishing is a
// best-effort accelerator the mail path never depends on, so an absent or failing
// relay simply leaves consumers on their existing poll cadence.
var changePublisher atomic.Pointer[func(ChangeEvent)]

// SetChangePublisher installs the push hook that open stores call after a committed
// mutation. A daemon calls it once at startup; passing nil disables push (the
// poll-only baseline). Stores already open observe the change on their next call.
func SetChangePublisher(fn func(ChangeEvent)) {
	if fn == nil {
		changePublisher.Store(nil)
		return
	}
	changePublisher.Store(&fn)
}

// publishChange best-effort-notifies the central relay that this mailbox changed,
// so a consumer's long-poll wakes and re-runs its authoritative diff. It MUST be
// called only AFTER the mutating transaction commits: a pre-commit wake would let
// the consumer diff before the row is visible and miss the change until its next
// poll. A nil hook is a no-op, identical to the pre-push behaviour.
func (s *Store) publishChange(op string, cn uint64, mid string) {
	if p := changePublisher.Load(); p != nil {
		(*p)(ChangeEvent{MailboxDir: s.dir, Op: op, CN: cn, Mid: mid})
	}
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
	// kind distinguishes a private mailbox from a per-domain public-folder
	// store. It selects which built-in hierarchy a fresh store is seeded with
	// and which IPM subtree the folder API roots at.
	kind storeKind
	// logger receives store infrastructure failures (SQL/IO errors); nil
	// disables logging. Stamped from defaultLogger at Open.
	logger *logging.Logger
}

// storeKind distinguishes a private mailbox store from a per-domain public-folder
// store. The two seed different built-in hierarchies and root their folder API at
// different IPM subtree ids (PrivateFIDIPMSubtree 0x09 vs PublicFIDIPMSubtree
// 0x02), so the store must remember which it is for every open, not just at seed.
type storeKind int

const (
	storePrivate storeKind = iota // private mailbox (default)
	storePublic                   // per-domain public-folder store
)

// ipmSubtree returns the folder id of this store's IPM subtree — the container
// whose children are the user-visible folders. It differs by store kind, so the
// folder API (CreateFolder/ListFolders/FolderByName) roots at the correct subtree
// for a private mailbox or a public-folder store.
func (s *Store) ipmSubtree() int64 {
	if s.kind == storePublic {
		return int64(mapi.PublicFIDIPMSubtree)
	}
	return int64(mapi.PrivateFIDIPMSubtree)
}

// Dir returns the mailbox directory this store is rooted at — its stable
// physical identity. Two stores opened over the same mailbox report the same
// Dir even though they are distinct handles, so a caller can tell whether two
// handles address the same physical mailbox (as opposed to comparing the
// *Store pointers, which differ per Open).
func (s *Store) Dir() string { return s.dir }

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
	return openKind(dir, true, storePrivate)
}

// OpenPublic opens the per-domain public-folder store rooted at dir, creating and
// seeding it if absent. A fresh store is provisioned with the public-folder
// hierarchy (Root Container / IPM_SUBTREE / NON_IPM_SUBTREE / EFORMS REGISTRY)
// instead of a private mailbox's folders, and the store's folder API roots at the
// public IPM subtree. dir is the domain's public-store directory (Config.HomedirFor).
func OpenPublic(dir string) (*Store, error) {
	return openKind(dir, true, storePublic)
}

// OpenPublicExisting opens a per-domain public-folder store only when it already
// exists, never creating one. It returns ErrNotProvisioned when no store has been
// provisioned at dir, so a read surface can distinguish a domain whose public
// folders are enabled (store present) from one where the feature is simply absent,
// without seeding an empty store as a side effect.
func OpenPublicExisting(dir string) (*Store, error) {
	if _, err := os.Stat(filepath.Join(dir, "objects.sqlite3")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotProvisioned
		}
		return nil, err
	}
	return OpenPublic(dir)
}

// open is the shared constructor for a private-kind store. seedBuiltins selects
// whether a fresh object store gets the default folder hierarchy; tests open a
// bare store to exercise allocators and property tables on a controlled baseline.
func open(dir string, seedBuiltins bool) (*Store, error) {
	return openKind(dir, seedBuiltins, storePrivate)
}

// openKind is the underlying constructor. kind selects the private mailbox or the
// public-folder hierarchy for a fresh store and is remembered for the store's
// lifetime so the folder API roots at the correct IPM subtree.
func openKind(dir string, seedBuiltins bool, kind storeKind) (*Store, error) {
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
	s := &Store{dir: dir, objdb: objdb, idxdb: idxdb, seedBuiltins: seedBuiltins, kind: kind, logger: defaultLogger.Load()}
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
	switch {
	case err == sql.ErrNoRows:
		// Fresh store: create the baseline schema, stamp its version, and seed.
		for _, stmt := range objectBaseline {
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
			if s.kind == storePublic {
				if err := s.seedPublicStore(guid); err != nil {
					return err
				}
			} else if err := s.seedMailbox(guid); err != nil {
				return err
			}
		}
	case err != nil:
		return err
	default:
		// Existing store: a version below the baseline is a pre-migration dev
		// schema (disposable, never deployed), so it is still refused. Versions at
		// or above the baseline are carried forward by the runner below.
		var v int
		if err := s.objdb.QueryRow(
			`SELECT config_value FROM configurations WHERE config_id=?`, cfgSchemaVersion).Scan(&v); err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
		if v < objectSchemaVersion {
			return fmt.Errorf("schema version %d unsupported (want %d)", v, objectSchemaVersion)
		}
	}
	// Fresh and existing stores converge here: apply any migrations beyond the
	// baseline once, and refuse a store recorded newer than this binary.
	return migrate.Run(context.Background(), s.objectDriver(), objectSchemaVersion, objectMigrations)
}

// objectDriver migrates objects.sqlite3, whose schema version lives in the
// configurations table rather than PRAGMA user_version.
func (s *Store) objectDriver() *migrate.SQLiteDriver {
	return &migrate.SQLiteDriver{
		DB: s.objdb,
		Ver: migrate.SQLiteVersion{
			Read: func(ctx context.Context, c *sql.Conn) (int, error) {
				var v int
				err := c.QueryRowContext(ctx,
					`SELECT config_value FROM configurations WHERE config_id=?`, cfgSchemaVersion).Scan(&v)
				return v, err
			},
			Write: func(ctx context.Context, c *sql.Conn, v int) error {
				_, err := c.ExecContext(ctx,
					`UPDATE configurations SET config_value=? WHERE config_id=?`, v, cfgSchemaVersion)
				return err
			},
		},
	}
}

func (s *Store) ensureIndexSchema() error {
	var name string
	err := s.idxdb.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='folders'`).Scan(&name)
	switch {
	case err == sql.ErrNoRows:
		// Fresh index: create the baseline schema and stamp its version.
		for _, stmt := range indexBaseline {
			if _, err := s.idxdb.Exec(stmt); err != nil {
				return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
			}
		}
		// PRAGMA does not accept bound parameters; the value is a trusted constant.
		if _, err := s.idxdb.Exec(fmt.Sprintf("PRAGMA user_version=%d", indexSchemaVersion)); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		// Existing index: refuse a pre-baseline dev schema; carry the rest forward.
		var v int
		if err := s.idxdb.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
			return err
		}
		if v < indexSchemaVersion {
			return fmt.Errorf("schema version %d unsupported (want %d)", v, indexSchemaVersion)
		}
	}
	// Fresh and existing indexes converge here: apply any migrations beyond the
	// baseline once, and refuse an index recorded newer than this binary.
	return migrate.Run(context.Background(),
		&migrate.SQLiteDriver{DB: s.idxdb, Ver: migrate.UserVersion()}, indexSchemaVersion, indexMigrations)
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
