package objectstore

import (
	"database/sql"
	"errors"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// A search folder holds no messages of its own. Its criteria name a restriction
// and the folders it looks in, and its contents are the live messages in those
// folders that match. Every protocol reads the same criteria and the same
// result: a client's search folder, a webmail saved search and an EWS search
// folder are one kind of store object.

// Errors a search folder operation reports.
var (
	// ErrNotSearchFolder is returned when search criteria are set on or read from
	// a folder that is not a search folder.
	ErrNotSearchFolder = errors.New("objectstore: not a search folder")
	// ErrSearchScope is returned when a search would look in a folder that holds
	// the search folder itself, so the search would include its own results.
	ErrSearchScope = errors.New("objectstore: search scope includes the search folder")
	// ErrSearchNotInitialized is returned when a change keeps the restriction or
	// the scope of a search folder that has never been given one.
	ErrSearchNotInitialized = errors.New("objectstore: search folder has no criteria")
)

// sqlQuery is satisfied by both *sql.DB and *sql.Tx, so a search is evaluated
// the same way for a read and inside the transaction that snapshots it.
type sqlQuery interface {
	sqlExec
	Query(query string, args ...any) (*sql.Rows, error)
}

// SearchCriteria is what a search folder searches for: the restriction a
// message must match, the folders the search looks in, and the search flags.
type SearchCriteria struct {
	Restriction *mapi.Restriction
	Scope       []int64
	Flags       uint32
}

// CreateSearchFolder creates an empty search folder named name under parent and
// returns its id. The folder matches nothing until its criteria are set.
func (s *Store) CreateSearchFolder(parent int64, name string) (int64, error) {
	replica, err := s.replicaGUID()
	if err != nil {
		return 0, err
	}
	tx, err := s.objdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	fid, err := allocateEID(tx)
	if err != nil {
		return 0, err
	}
	cn, err := createSearchFolder(tx, replica, mapi.UnixToNTTime(time.Now()), fid, uint64(parent), name) // #nosec G115 -- a folder id is never negative
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.publishChange("folder", cn, "")
	return int64(fid), nil // #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits
}

// IsSearchFolder reports whether fid is a live search folder.
func (s *Store) IsSearchFolder(fid int64) (bool, error) {
	var isSearch int64
	err := s.objdb.QueryRow(`SELECT COALESCE(is_search, 0) FROM folders WHERE folder_id=? AND is_deleted=0`, fid).Scan(&isSearch)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return isSearch != 0, err
}

// SearchFolderChildren returns the live search folders directly under parent.
func (s *Store) SearchFolderChildren(parent int64) ([]FolderInfo, error) {
	rows, err := s.objdb.Query(
		`SELECT folder_id FROM folders WHERE parent_id=? AND is_search=1 AND is_deleted=0 ORDER BY folder_id`, parent)
	if err != nil {
		return nil, err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return nil, err
	}
	out := make([]FolderInfo, 0, len(ids))
	for _, id := range ids {
		name, err := s.folderDisplayName(id)
		if err != nil {
			return nil, err
		}
		p := parent
		out = append(out, FolderInfo{ID: id, ParentID: &p, DisplayName: name})
	}
	return out, nil
}

// normalizeSearchFlags fills in the defaults a client may leave out: a search
// that names neither restart nor stop is stopped, and one that names neither
// recursive nor shallow is shallow ([MS-OXCFOLD] 2.2.1.4.1).
func normalizeSearchFlags(flags uint32) uint32 {
	if flags&(mapi.SearchRestart|mapi.SearchStop) == 0 {
		flags |= mapi.SearchStop
	}
	if flags&(mapi.SearchRecursive|mapi.SearchShallow) == 0 {
		flags |= mapi.SearchShallow
	}
	return flags
}

// SetSearchCriteria sets a search folder's criteria. A nil restriction keeps the
// stored one and an empty scope keeps the stored scope; either needs criteria
// set before. A scope folder that does not exist is left out. A search that is
// restarted as a static search takes a snapshot of its results now; any other
// running search is evaluated whenever its contents are read.
func (s *Store) SetSearchCriteria(fid int64, c SearchCriteria) error {
	if err := s.requireSearchFolder(fid); err != nil {
		return err
	}
	flags := normalizeSearchFlags(c.Flags)
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := loadSearchCriteria(tx, fid)
	if err != nil {
		return err
	}
	next, err := mergeSearchCriteria(old, c, flags)
	if err != nil {
		return err
	}
	if next.Scope, err = checkSearchScope(tx, fid, next.Scope); err != nil {
		return err
	}
	if err := storeSearchCriteria(tx, fid, next); err != nil {
		return err
	}
	if err := s.snapshotStaticSearch(tx, fid, next); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.publishChange("folder", 0, "")
	return nil
}

// mergeSearchCriteria applies a change to stored criteria: the change's flags
// always apply, and a nil restriction or an empty scope keeps the stored one,
// which a folder without criteria does not have.
func mergeSearchCriteria(old, c SearchCriteria, flags uint32) (SearchCriteria, error) {
	if (c.Restriction == nil || len(c.Scope) == 0) && old.Restriction == nil {
		return SearchCriteria{}, ErrSearchNotInitialized
	}
	next := SearchCriteria{Restriction: c.Restriction, Scope: c.Scope, Flags: flags}
	if next.Restriction == nil {
		next.Restriction = old.Restriction
	}
	if len(next.Scope) == 0 {
		next.Scope = old.Scope
	}
	return next, nil
}

// GetSearchCriteria returns a search folder's criteria and its search state
// ([MS-OXCFOLD] 2.2.1.5.2). A search folder whose criteria were never set
// returns a nil restriction, no scope and no state.
func (s *Store) GetSearchCriteria(fid int64) (SearchCriteria, uint32, error) {
	if err := s.requireSearchFolder(fid); err != nil {
		return SearchCriteria{}, 0, err
	}
	c, err := loadSearchCriteria(s.objdb, fid)
	if err != nil || c.Restriction == nil {
		return SearchCriteria{}, 0, err
	}
	return c, searchState(c.Flags), nil
}

// searchState derives the state a search folder reports from its flags. The
// store evaluates a running search when its contents are read, so no search is
// ever still populating.
func searchState(flags uint32) uint32 {
	var state uint32
	if flags&mapi.SearchRestart != 0 {
		if flags&mapi.SearchStatic != 0 {
			state |= mapi.SearchStateComplete | mapi.SearchStateStatic
		} else {
			state |= mapi.SearchStateRunning
		}
	}
	if flags&mapi.SearchRecursive != 0 {
		state |= mapi.SearchStateRecursive
	}
	return state
}

// SearchFolderMessageIDs returns the ids of the messages a search folder holds:
// for a stopped search none, for a static search the snapshot taken when it was
// restarted less the messages deleted since, and for a running search the live
// messages in its scope that match its restriction now.
func (s *Store) SearchFolderMessageIDs(fid int64) ([]int64, error) {
	if err := s.requireSearchFolder(fid); err != nil {
		return nil, err
	}
	c, err := loadSearchCriteria(s.objdb, fid)
	if err != nil || c.Restriction == nil || c.Flags&mapi.SearchRestart == 0 {
		return nil, err
	}
	if c.Flags&mapi.SearchStatic != 0 {
		rows, err := s.objdb.Query(
			`SELECT r.message_id FROM search_result r JOIN messages m ON m.message_id=r.message_id
			 WHERE r.folder_id=? AND m.is_deleted=0 ORDER BY r.message_id`, fid)
		if err != nil {
			return nil, err
		}
		return scanIDs(rows)
	}
	return s.matchSearch(s.objdb, c)
}

// requireSearchFolder reports ErrNotFound for a missing folder and
// ErrNotSearchFolder for a folder that is not a search folder.
func (s *Store) requireSearchFolder(fid int64) error {
	ok, err := s.IsSearchFolder(fid)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotSearchFolder
	}
	return nil
}

// loadSearchCriteria reads the stored criteria of a search folder. A folder
// with no stored restriction returns a nil one.
func loadSearchCriteria(q sqlQuery, fid int64) (SearchCriteria, error) {
	var (
		flags sql.NullInt64
		blob  []byte
		c     SearchCriteria
	)
	if err := q.QueryRow(`SELECT search_flags, search_criteria FROM folders WHERE folder_id=?`, fid).Scan(&flags, &blob); err != nil {
		return c, err
	}
	c.Flags = uint32(flags.Int64) // #nosec G115 -- the flags are a 32-bit field
	if len(blob) == 0 {
		return c, nil
	}
	r, err := ext.NewPull(blob, ruleExtFlags).Restriction()
	if err != nil {
		return c, err
	}
	c.Restriction = &r
	rows, err := q.Query(`SELECT included_fid FROM search_scopes WHERE folder_id=? ORDER BY rowid`, fid)
	if err != nil {
		return c, err
	}
	c.Scope, err = scanIDs(rows)
	return c, err
}

// storeSearchCriteria writes a search folder's flags, restriction and scope.
func storeSearchCriteria(tx *sql.Tx, fid int64, c SearchCriteria) error {
	blob, err := marshalRestriction(c.Restriction)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE folders SET search_flags=?, search_criteria=? WHERE folder_id=?`, int64(c.Flags), blob, fid); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM search_scopes WHERE folder_id=?`, fid); err != nil {
		return err
	}
	for _, scope := range c.Scope {
		if _, err := tx.Exec(`INSERT INTO search_scopes (folder_id, included_fid) VALUES (?, ?)`, fid, scope); err != nil {
			return err
		}
	}
	return nil
}

// checkSearchScope drops the scope folders that do not exist and refuses a
// scope folder that is the search folder or one of its ancestors, since the
// search would then look at its own results.
func checkSearchScope(q sqlExec, fid int64, scope []int64) ([]int64, error) {
	ancestors, err := folderAncestry(q, fid)
	if err != nil {
		return nil, err
	}
	var kept []int64
	for _, f := range scope {
		if _, ok := ancestors[f]; ok {
			return nil, ErrSearchScope
		}
		var n int
		if err := q.QueryRow(`SELECT COUNT(*) FROM folders WHERE folder_id=? AND is_deleted=0`, f).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			kept = append(kept, f)
		}
	}
	return kept, nil
}

// folderAncestry returns fid and every folder above it.
func folderAncestry(q sqlExec, fid int64) (map[int64]struct{}, error) {
	seen := map[int64]struct{}{}
	id := sql.NullInt64{Int64: fid, Valid: true}
	for range maxFolderDepth {
		if !id.Valid {
			break
		}
		if _, dup := seen[id.Int64]; dup {
			break
		}
		seen[id.Int64] = struct{}{}
		var parent sql.NullInt64
		err := q.QueryRow(`SELECT parent_id FROM folders WHERE folder_id=?`, id.Int64).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		id = parent
	}
	return seen, nil
}

// snapshotStaticSearch replaces a search folder's stored results: a restarted
// static search stores what matches now, and every other search stores nothing,
// since a running search is evaluated when it is read.
func (s *Store) snapshotStaticSearch(tx *sql.Tx, fid int64, c SearchCriteria) error {
	if _, err := tx.Exec(`DELETE FROM search_result WHERE folder_id=?`, fid); err != nil {
		return err
	}
	if c.Flags&(mapi.SearchRestart|mapi.SearchStatic) != mapi.SearchRestart|mapi.SearchStatic {
		return nil
	}
	ids, err := s.matchSearch(tx, c)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT INTO search_result (folder_id, message_id) VALUES (?, ?)`, fid, id); err != nil {
			return err
		}
	}
	return nil
}

// matchSearch returns the live, non-FAI messages in the search's scope that
// match its restriction, in id order. A recursive search also looks in every
// folder below each scope folder.
func (s *Store) matchSearch(q sqlQuery, c SearchCriteria) ([]int64, error) {
	folders, err := searchFolders(q, c)
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, f := range folders {
		ids, err := s.matchFolder(q, f, *c.Restriction)
		if err != nil {
			return nil, err
		}
		out = append(out, ids...)
	}
	return out, nil
}

// searchFolders expands a search's scope into the folders it looks in, each
// once: the scope folders, and for a recursive search their live descendants.
func searchFolders(q sqlQuery, c SearchCriteria) ([]int64, error) {
	seen := map[int64]struct{}{}
	var out []int64
	add := func(id int64) {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	for _, f := range c.Scope {
		add(f)
		if c.Flags&mapi.SearchRecursive == 0 {
			continue
		}
		rows, err := q.Query(`
			WITH RECURSIVE sub(folder_id) AS (
				SELECT folder_id FROM folders WHERE parent_id=? AND is_deleted=0
				UNION ALL
				SELECT f.folder_id FROM folders f JOIN sub ON f.parent_id=sub.folder_id WHERE f.is_deleted=0
			)
			SELECT folder_id FROM sub ORDER BY folder_id`, f)
		if err != nil {
			return nil, err
		}
		ids, err := scanIDs(rows)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			add(id)
		}
	}
	return out, nil
}

// matchFolder returns the live, non-FAI messages of one folder that match r.
// The message size is set on the property bag, as a rule condition sees it,
// because the store keeps it on the message row.
func (s *Store) matchFolder(q sqlQuery, fid int64, r mapi.Restriction) ([]int64, error) {
	rows, err := q.Query(
		`SELECT message_id, message_size FROM messages WHERE parent_fid=? AND is_deleted=0 AND COALESCE(is_associated, 0)=0 ORDER BY message_id`, fid)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, size int64 }
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.size); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []int64
	for _, c := range cands {
		props, err := s.GetMessageProperties(c.id)
		if err != nil {
			return nil, err
		}
		props.Set(mapi.PrMessageSize, clampLong(c.size))
		if evalRestriction(r, props) {
			out = append(out, c.id)
		}
	}
	return out, nil
}
