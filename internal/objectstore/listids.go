package objectstore

import "database/sql"

// A caller that only has to ADDRESS a folder's messages, and that reads each
// message's properties from the store when it serves one, needs the ids and
// nothing else. ListMessages and ListSoftDeletedInfo hand back a MessageInfo per
// row, whose subject, sender and preview strings are the bulk of the memory; the
// two readers below return the same rows in the same order as bare ids.

// scanIDs reads a single-column id result set.
func scanIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListMessageIDs returns a folder's message ids in ascending IMAP UID order, the
// order ListMessages returns its rows in. It reads one indexed column instead of
// every index projection.
func (s *Store) ListMessageIDs(folderID int64) ([]int64, error) {
	rows, err := s.idxdb.Query(
		`SELECT message_id FROM messages WHERE folder_id=? ORDER BY uid`, folderID)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// ListSoftDeletedIDs returns a folder's soft-deleted message ids in the order
// ListSoftDeletedInfo returns its rows in. The dumpster rows no longer live in the
// IMAP index, so they are read from the object store, exactly as that function does.
func (s *Store) ListSoftDeletedIDs(folderID int64) ([]int64, error) {
	rows, err := s.objdb.Query(
		`SELECT message_id FROM messages WHERE parent_fid=? AND is_deleted=1 ORDER BY message_id`, folderID)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}
