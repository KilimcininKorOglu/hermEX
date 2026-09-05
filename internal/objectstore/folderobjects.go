package objectstore

import (
	"database/sql"
	"time"

	"hermex/internal/mapi"
)

// FolderObject is one stored object as the object/DAV layer sees it: its EID plus
// an opaque monotonic version (the change number) used as the DAV ETag and the
// basis for collection sync. Unlike MessageInfo, an IMAP-index projection with
// RFC822 envelope fields, a FolderObject is read straight from the object store,
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
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		out = append(out, FolderObject{ID: id, ChangeNumber: uint64(cn)})
	}
	return out, rows.Err()
}

// AppointmentTimeTags resolves the two PtSysTime named properties an appointment's
// span lives in, PidLidAppointmentStartWhole and PidLidAppointmentEndWhole, to this
// store's tags. It reports ok=false when the store has never allocated them, which
// is the state of a mailbox that holds no appointment yet; a caller then falls back
// to its unwindowed path rather than filtering on a tag that matches nothing.
// It never allocates, because a read must not write to the store.
func (s *Store) AppointmentTimeTags() (start, end mapi.PropTag, ok bool) {
	ids, err := s.GetNamedPropIDs(false, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole,
		mapi.NameAppointmentEndWhole,
	})
	if err != nil || len(ids) != 2 || ids[0] == 0 || ids[1] == 0 {
		return 0, 0, false
	}
	return mapi.MakeTag(ids[0], mapi.PtSysTime), mapi.MakeTag(ids[1], mapi.PtSysTime), true
}

// ListFolderObjectsInWindow returns the folder's live, non-associated objects whose
// [startTag, endTag] span can overlap [start, end), resolved by the store instead of
// by the caller. It is the enumeration primitive for a calendar range request: the
// alternative is to list every object in the folder and read each one's properties
// back to compare two times, which costs one query per object and materializes
// property bags for objects the caller is about to discard.
//
// The window is DELIBERATELY WIDER than any caller's own filter, so it can never
// change a result. An object whose start property is absent is returned, because the
// callers disagree about what an undated object means and only they can decide.
// endTag is optional (a zero tag, or a missing value, reads as an instant at start).
// A caller keeps its own exact filter and applies it to what comes back; this only
// removes objects every filter would remove anyway.
//
// startTag and endTag are PtSysTime named-property tags, stored as NT FILETIME
// integers, which is what makes the comparison a plain SQL range predicate.
func (s *Store) ListFolderObjectsInWindow(folderID int64, startTag, endTag mapi.PropTag, start, end time.Time) ([]FolderObject, error) {
	// #nosec G115 -- an NT FILETIME crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	winStart, winEnd := int64(mapi.UnixToNTTime(start)), int64(mapi.UnixToNTTime(end))
	rows, err := s.objdb.Query(
		`SELECT m.message_id, m.change_number
		   FROM messages m
		   LEFT JOIN message_properties ps ON ps.message_id = m.message_id AND ps.proptag = ?
		   LEFT JOIN message_properties pe ON pe.message_id = m.message_id AND pe.proptag = ?
		  WHERE m.parent_fid = ? AND m.is_deleted = 0 AND m.is_associated = 0
		    AND (ps.propval IS NULL
		         OR (ps.propval < ? AND COALESCE(pe.propval, ps.propval) >= ?))
		  ORDER BY m.message_id`,
		int64(uint32(startTag)), int64(uint32(endTag)), folderID, winEnd, winStart)
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
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		out = append(out, FolderObject{ID: id, ChangeNumber: uint64(cn)})
	}
	return out, rows.Err()
}

// FolderMaxChangeNumber returns the highest change number among a folder's live,
// non-associated objects, or 0 when the folder holds none. Change numbers are
// allocated from a store-wide monotonic counter, so this value advances whenever
// an object in the folder is created or modified; it is the basis for a
// collection's CTag and CardDAV/CalDAV sync-token. (It does not advance on
// deletion, the store hard-deletes without a tombstone, so incremental delete
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
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	return uint64(max.Int64), nil
}

// FolderObjectsSyncMax returns the highest change number among a folder's
// non-associated objects INCLUDING soft-deleted ones. A soft delete bumps the
// change number on the dumpster row, so this value (unlike FolderMaxChangeNumber)
// advances on a deletion. It is the CTag and sync-token basis once deletions are
// reported: a sync-token built from it never re-reports a tombstone, and a CTag
// built from it changes when a member is removed.
func (s *Store) FolderObjectsSyncMax(folderID int64) (uint64, error) {
	var max sql.NullInt64
	if err := s.objdb.QueryRow(
		`SELECT MAX(change_number) FROM messages
		 WHERE parent_fid=? AND is_associated=0`, folderID).Scan(&max); err != nil {
		return 0, err
	}
	if !max.Valid {
		return 0, nil
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	return uint64(max.Int64), nil
}

// DeletedObjectsSince returns a folder's soft-deleted non-associated objects whose
// deletion advanced the change number past sinceCN, the CalDAV/CardDAV
// sync-collection tombstones (RFC 6578). Each is reported to the client as a 404
// member so it removes the object locally.
func (s *Store) DeletedObjectsSince(folderID int64, sinceCN uint64) ([]FolderObject, error) {
	rows, err := s.objdb.Query(
		`SELECT message_id, change_number FROM messages
		 WHERE parent_fid=? AND is_deleted=1 AND is_associated=0 AND change_number>?
		 ORDER BY message_id`,
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		folderID, int64(sinceCN))
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
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		out = append(out, FolderObject{ID: id, ChangeNumber: uint64(cn)})
	}
	return out, rows.Err()
}

// FolderMessageChangeNumbers returns each live, non-associated message's objectstore
// id mapped to its latest modification counter, the per-message snapshot a
// notification poll diffs against. Against a prior snapshot: a new id is a create, a
// vanished id a delete, and a changed counter a modify. The counter is
// MAX(change_number, read_cn): both columns draw from the one mailbox change-number
// counter, but a read/unread flip advances read_cn (not change_number), so taking
// the max lets the poll see a read-state change as a modify too. (Neither counter
// advances on a hard delete, the store keeps no tombstone, so deletes are detected
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
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		out[id] = uint64(cn)
	}
	return out, rows.Err()
}

// FindAssociatedByClass returns the id of the folder's associated (FAI) message
// carrying the given message class, the way a configuration item is addressed:
// nobody holds an id for it, so it is found by what it is. ok is false when the
// folder holds none.
//
// An FAI message is invisible to ListFolderObjects by design, so this is the only
// way to reach one. A folder holding several of one class returns the lowest id,
// which is the oldest, because a duplicate is a client's mistake and the first one
// written is the one every other client already agreed on.
func (s *Store) FindAssociatedByClass(folderID int64, class string) (int64, bool, error) {
	rows, err := s.objdb.Query(
		`SELECT m.message_id
		   FROM messages m
		   JOIN message_properties p ON p.message_id = m.message_id AND p.proptag = ?
		  WHERE m.parent_fid = ? AND m.is_deleted = 0 AND m.is_associated = 1
		    AND p.propval = ?
		  ORDER BY m.message_id`,
		int64(uint32(mapi.PrMessageClass)), folderID, class)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, false, rows.Err()
	}
	var id int64
	if err := rows.Scan(&id); err != nil {
		return 0, false, err
	}
	return id, true, rows.Err()
}
