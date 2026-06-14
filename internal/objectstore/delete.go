package objectstore

import (
	"database/sql"
	"errors"
	"os"
)

// DeleteMessage permanently removes a message from a folder by its IMAP UID:
// the object (foreign-key cascade drops its property bags, recipients,
// attachments, and time-index row), the index row and mapping, and the cached
// eml. Content files are left in place — they are content-addressed and may be
// shared with other messages, so reclaiming them is a separate sweep. It
// reports ErrNotFound when no such message exists.
func (s *Store) DeleteMessage(folderID int64, uid uint32) error {
	var messageID int64
	var mid string
	err := s.idxdb.QueryRow(
		`SELECT message_id, mid_string FROM messages WHERE folder_id=? AND uid=?`,
		folderID, int64(uid)).Scan(&messageID, &mid)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	// Remove the object first (its cascade drops everything it owns), then the
	// index rows; an interruption between leaves an index row pointing at a
	// gone object, which a folder reindex prunes.
	if _, err := s.objdb.Exec(`DELETE FROM messages WHERE message_id=?`, messageID); err != nil {
		return err
	}
	if _, err := s.idxdb.Exec(`DELETE FROM messages WHERE message_id=?`, messageID); err != nil {
		return err
	}
	if _, err := s.idxdb.Exec(`DELETE FROM mapping WHERE message_id=?`, messageID); err != nil {
		return err
	}
	// The cached eml is orphaned once the index row is gone; best-effort cleanup.
	_ = os.Remove(s.emlPath(mid))
	return nil
}

// DeleteObject removes a message from the object store by its EID, regardless of
// whether it was ever placed in the IMAP index. Mail is indexed and deleted by
// IMAP UID (DeleteMessage); non-mail objects (contacts, calendar items) live
// only in the object store, so the DAV layer deletes them here. The object's
// foreign-key cascade drops its property bags, recipients, and attachments; any
// stale index row, mapping, and cached eml are cleaned up too. It reports
// ErrNotFound when no such object exists.
func (s *Store) DeleteObject(messageID int64) error {
	var mid string
	err := s.objdb.QueryRow(`SELECT mid_string FROM messages WHERE message_id=?`, messageID).Scan(&mid)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := s.objdb.Exec(`DELETE FROM messages WHERE message_id=?`, messageID); err != nil {
		return err
	}
	if _, err := s.idxdb.Exec(`DELETE FROM messages WHERE message_id=?`, messageID); err != nil {
		return err
	}
	if _, err := s.idxdb.Exec(`DELETE FROM mapping WHERE message_id=?`, messageID); err != nil {
		return err
	}
	_ = os.Remove(s.emlPath(mid))
	return nil
}
