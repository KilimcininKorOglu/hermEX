package objectstore

import "database/sql"

// FolderObject is one stored object as the object/DAV layer sees it: its EID plus
// an opaque monotonic version (the change number) used as the DAV ETag and the
// basis for collection sync. Unlike MessageInfo — an IMAP-index projection with
// RFC822 envelope fields — a FolderObject is read straight from the object store,
// so it sees objects that were never indexed for IMAP (contacts, calendar items).
type FolderObject struct {
	ID           int64  // message EID (object store primary key)
	ChangeNumber uint64 // monotonic per-write version; the DAV ETag and sync basis
}

// ListFolderObjects returns a folder's live, non-associated object messages read
// directly from the object store (not the IMAP index), ordered by EID. It is the
// enumeration primitive for non-mail collections such as CardDAV address books
// and CalDAV calendars: those items are created with CreateMessage and never
// enter the IMAP index, so ListMessages does not see them. Folder-associated
// information and deleted objects are excluded.
func (s *Store) ListFolderObjects(folderID int64) ([]FolderObject, error) {
	rows, err := s.objdb.Query(
		`SELECT message_id, change_number FROM messages
		 WHERE parent_fid=? AND is_deleted=0 AND is_associated=0
		 ORDER BY message_id`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FolderObject
	for rows.Next() {
		var id, cn int64
		if err := rows.Scan(&id, &cn); err != nil {
			return nil, err
		}
		out = append(out, FolderObject{ID: id, ChangeNumber: uint64(cn)})
	}
	return out, rows.Err()
}

// FolderMaxChangeNumber returns the highest change number among a folder's live,
// non-associated objects, or 0 when the folder holds none. Change numbers are
// allocated from a store-wide monotonic counter, so this value advances whenever
// an object in the folder is created or modified; it is the basis for a
// collection's CTag and CardDAV/CalDAV sync-token. (It does not advance on
// deletion — the store hard-deletes without a tombstone — so incremental delete
// reporting is out of scope for the first sync implementation.)
func (s *Store) FolderMaxChangeNumber(folderID int64) (uint64, error) {
	var max sql.NullInt64
	if err := s.objdb.QueryRow(
		`SELECT MAX(change_number) FROM messages
		 WHERE parent_fid=? AND is_deleted=0 AND is_associated=0`, folderID).Scan(&max); err != nil {
		return 0, err
	}
	if !max.Valid {
		return 0, nil
	}
	return uint64(max.Int64), nil
}

// FolderMessageChangeNumbers returns each live, non-associated message's objectstore
// id mapped to its latest modification counter — the per-message snapshot a
// notification poll diffs against. Against a prior snapshot: a new id is a create, a
// vanished id a delete, and a changed counter a modify. The counter is
// MAX(change_number, read_cn): both columns draw from the one mailbox change-number
// counter, but a read/unread flip advances read_cn (not change_number), so taking
// the max lets the poll see a read-state change as a modify too. (Neither counter
// advances on a hard delete — the store keeps no tombstone — so deletes are detected
// by the id's absence, matching FolderMaxChangeNumber's contract.)
func (s *Store) FolderMessageChangeNumbers(folderID int64) (map[int64]uint64, error) {
	rows, err := s.objdb.Query(
		`SELECT message_id, MAX(change_number, COALESCE(read_cn, 0)) FROM messages
		 WHERE parent_fid=? AND is_deleted=0 AND is_associated=0`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]uint64)
	for rows.Next() {
		var id, cn int64
		if err := rows.Scan(&id, &cn); err != nil {
			return nil, err
		}
		out[id] = uint64(cn)
	}
	return out, rows.Err()
}
