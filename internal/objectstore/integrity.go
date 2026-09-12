package objectstore

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// The two SQLite files a mailbox holds. They are named here because the
// integrity check addresses them as files, before any Store exists.
const (
	objectsDBName = "objects.sqlite3"
	indexDBName   = "imapindex.sqlite3"
)

// FindingKind names one class of problem CheckMailbox reports.
type FindingKind string

const (
	// FindingUnreadableDatabase is a database that could not be opened or queried.
	FindingUnreadableDatabase FindingKind = "unreadable-database"
	// FindingCorruptDatabase is a database SQLite's own integrity check rejects.
	FindingCorruptDatabase FindingKind = "corrupt-database"
	// FindingOrphanIndexRow is an IMAP index row whose object store message is gone.
	FindingOrphanIndexRow FindingKind = "orphan-index-row"
	// FindingMissingIndexRow is a live message in an indexed folder that the IMAP
	// index does not list, so IMAP and POP3 cannot see it.
	FindingMissingIndexRow FindingKind = "missing-index-row"
	// FindingMissingContent is a property that names a content file the cid
	// directory does not hold, so the property reads as an error.
	FindingMissingContent FindingKind = "missing-content-file"
)

// Finding is one problem CheckMailbox found.
type Finding struct {
	Kind   FindingKind
	File   string // the file it concerns, relative to the mailbox directory
	Count  int    // how many rows or files carry it; 1 for a whole-file problem
	Detail string // SQLite's own message, or one example id
}

// String renders a finding as one operator-readable line.
func (f Finding) String() string {
	return fmt.Sprintf("%s %s count=%d %s", f.Kind, f.File, f.Count, f.Detail)
}

// CheckMailbox reports every problem it can see in the mailbox rooted at dir and
// changes nothing, not even the SQLite journal: both databases are opened
// read-only. An empty result means the check found nothing, which is the only
// claim it makes.
//
// A database SQLite rejects is reported on its own and its rows are not compared
// against anything, because a cross-file difference read out of a damaged file
// says nothing about the healthy one. Repair that first, then check again.
func CheckMailbox(dir string) ([]Finding, error) {
	if _, err := os.Stat(filepath.Join(dir, objectsDBName)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotProvisioned
		}
		return nil, err
	}
	objdb, objFindings := openForCheck(dir, objectsDBName)
	idxdb, idxFindings := openForCheck(dir, indexDBName)
	defer closeChecked(objdb)
	defer closeChecked(idxdb)

	out := append(objFindings, idxFindings...)
	if objdb == nil || idxdb == nil {
		return out, nil
	}
	cross, err := crossFindings(dir, objdb, idxdb)
	if err != nil {
		return out, err
	}
	return append(out, cross...), nil
}

// openForCheck opens one mailbox database read-only and runs SQLite's integrity
// check over it. It returns a usable handle only when the database is healthy, so
// the caller never queries a file SQLite has already rejected.
func openForCheck(dir, name string) (*sql.DB, []Finding) {
	db, err := sql.Open("sqlite", readOnlyDSN(filepath.Join(dir, name)))
	if err != nil {
		return nil, []Finding{{Kind: FindingUnreadableDatabase, File: name, Count: 1, Detail: err.Error()}}
	}
	problems, err := integrityProblems(db)
	if err != nil {
		_ = db.Close()
		return nil, []Finding{{Kind: FindingUnreadableDatabase, File: name, Count: 1, Detail: err.Error()}}
	}
	if len(problems) > 0 {
		_ = db.Close()
		return nil, []Finding{{Kind: FindingCorruptDatabase, File: name, Count: len(problems), Detail: problems[0]}}
	}
	return db, nil
}

// closeChecked closes a handle openForCheck may not have returned.
func closeChecked(db *sql.DB) {
	if db != nil {
		_ = db.Close()
	}
}

// readOnlyDSN opens a database for reading only. query_only refuses every write
// to the data, so a check cannot change the file it is inspecting. It deliberately
// does NOT use mode=ro: a WAL database needs its shared-memory index file, and a
// strictly read-only connection cannot create one when no daemon is running, which
// would make the check fail exactly on an idle mailbox.
func readOnlyDSN(path string) string {
	return "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=query_only(1)"
}

// integrityProblems runs SQLite's own integrity check and returns the messages it
// reports. A healthy database answers with the single row "ok".
func integrityProblems(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`PRAGMA integrity_check`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			return nil, err
		}
		if msg != "ok" {
			out = append(out, msg)
		}
	}
	return out, rows.Err()
}

// crossFindings compares the two healthy databases and the content directory
// against each other.
func crossFindings(dir string, objdb, idxdb *sql.DB) ([]Finding, error) {
	live, indexable, err := liveMessageIDs(objdb, idxdb)
	if err != nil {
		return nil, err
	}
	indexed, err := idSet(idxdb, `SELECT message_id FROM messages`)
	if err != nil {
		return nil, err
	}
	var out []Finding
	if f, ok := absentFinding(FindingOrphanIndexRow, indexDBName, indexed, live); ok {
		out = append(out, f)
	}
	if f, ok := absentFinding(FindingMissingIndexRow, objectsDBName, indexable, indexed); ok {
		out = append(out, f)
	}
	missing, err := missingContentFinding(dir, objdb)
	if err != nil {
		return nil, err
	}
	if missing.Count > 0 {
		out = append(out, missing)
	}
	return out, nil
}

// liveMessageIDs reads the object store's live message ids twice over: every one,
// and the subset that belongs to a folder the IMAP index carries. Only the second
// set is expected in the index, because calendar, contact, task and note objects
// are stored without an index row at all.
func liveMessageIDs(objdb, idxdb *sql.DB) (live, indexable map[int64]bool, err error) {
	indexedFolders, err := idSet(idxdb, `SELECT folder_id FROM folders`)
	if err != nil {
		return nil, nil, err
	}
	rows, err := objdb.Query(
		`SELECT message_id, parent_fid FROM messages WHERE is_deleted=0 AND is_associated=0`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	live, indexable = map[int64]bool{}, map[int64]bool{}
	for rows.Next() {
		var id, fid int64
		if err := rows.Scan(&id, &fid); err != nil {
			return nil, nil, err
		}
		live[id] = true
		if indexedFolders[fid] {
			indexable[id] = true
		}
	}
	return live, indexable, rows.Err()
}

// idSet reads a single-column id query into a set.
func idSet(db *sql.DB, query string) (map[int64]bool, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// absentFinding reports the ids of have that want does not hold, as one finding
// carrying the count and the lowest such id as an example.
func absentFinding(kind FindingKind, file string, have, want map[int64]bool) (Finding, bool) {
	f := Finding{Kind: kind, File: file}
	var example int64
	for id := range have {
		if want[id] {
			continue
		}
		f.Count++
		if example == 0 || id < example {
			example = id
		}
	}
	if f.Count == 0 {
		return Finding{}, false
	}
	f.Detail = fmt.Sprintf("first message_id %d", example)
	return f, true
}

// missingContentFinding reports the properties whose content file is gone. A
// property value is offloaded to a content-addressed file, so a missing file makes
// that body or attachment unreadable while the message itself still lists.
func missingContentFinding(dir string, objdb *sql.DB) (Finding, error) {
	refs, err := referencedContentIDs(objdb)
	if err != nil {
		return Finding{}, err
	}
	f := Finding{Kind: FindingMissingContent, File: "cid"}
	for cid := range refs {
		if _, err := os.Stat(cidFilePath(dir, cid)); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Finding{}, err
		}
		f.Count++
		if f.Detail == "" {
			f.Detail = "first content id " + cid
		}
	}
	return f, nil
}

// withMailboxExclusive runs fn while holding the mailbox exclusively, without
// opening it as a Store, and reports ErrMailboxBusy without running fn when
// anything else holds it. A repair works on a store this process cannot open (a
// damaged database refuses to open at all), so it cannot take the lock the usual
// way, through the Store it is repairing.
func withMailboxExclusive(dir string, fn func() error) error {
	// #nosec G304 -- the lock file is a fixed name inside the mailbox directory
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("%w: %s", ErrMailboxBusy, dir)
		}
		return err
	}
	return fn()
}
