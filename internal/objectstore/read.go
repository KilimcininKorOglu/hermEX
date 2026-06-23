package objectstore

import (
	"database/sql"
	"errors"
	"time"
)

// messageInfoCols is the index column list a MessageInfo is scanned from.
const messageInfoCols = `message_id, uid, received, size, read, replied, flagged, deleted, unsent, subject, sender`

// composeFlags builds the IMAP flag mask from the index's boolean flag columns.
func composeFlags(read, answered, flagged, deleted, draft int) int64 {
	var f int64
	if read != 0 {
		f |= FlagSeen
	}
	if answered != 0 {
		f |= FlagAnswered
	}
	if flagged != 0 {
		f |= FlagFlagged
	}
	if deleted != 0 {
		f |= FlagDeleted
	}
	if draft != 0 {
		f |= FlagDraft
	}
	return f
}

// scanMessageInfo scans a row of messageInfoCols into a MessageInfo.
func scanMessageInfo(sc interface{ Scan(...any) error }) (MessageInfo, error) {
	var (
		m                                       MessageInfo
		uid, received                           int64
		read, replied, flagged, deleted, unsent int
	)
	if err := sc.Scan(&m.ID, &uid, &received, &m.Size, &read, &replied, &flagged, &deleted, &unsent, &m.Subject, &m.Sender); err != nil {
		return MessageInfo{}, err
	}
	m.UID = uint32(uid)
	m.InternalDate = time.Unix(received, 0).UTC()
	m.Flags = composeFlags(read, replied, flagged, deleted, unsent)
	return m, nil
}

// ListMessages returns a folder's messages ordered by ascending IMAP UID.
func (s *Store) ListMessages(folderID int64) ([]MessageInfo, error) {
	rows, err := s.idxdb.Query(
		`SELECT `+messageInfoCols+` FROM messages WHERE folder_id=? ORDER BY uid`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageInfo
	for rows.Next() {
		m, err := scanMessageInfo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountMessages returns a folder's total message count and unread count, read
// directly from the IMAP index. The WHERE clause mirrors ListMessages exactly
// (folder id only, no flag filter), so a folder's reported counts always agree
// with the rows the listing enumerates. unread counts messages whose read flag
// is clear.
func (s *Store) CountMessages(folderID int64) (total, unread int, err error) {
	err = s.idxdb.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN read=0 THEN 1 ELSE 0 END), 0) FROM messages WHERE folder_id=?`,
		folderID).Scan(&total, &unread)
	return total, unread, err
}

// FolderSize returns the total byte size of the messages in a folder — the sum of
// their RFC 822 sizes — for the sidebar's per-folder size display.
func (s *Store) FolderSize(folderID int64) (int64, error) {
	var size int64
	err := s.idxdb.QueryRow(
		`SELECT COALESCE(SUM(size), 0) FROM messages WHERE folder_id=?`, folderID).Scan(&size)
	return size, err
}

// MessageByUID returns one message's metadata by folder and IMAP UID, reporting
// ErrNotFound when it does not exist.
func (s *Store) MessageByUID(folderID int64, uid uint32) (MessageInfo, error) {
	row := s.idxdb.QueryRow(
		`SELECT `+messageInfoCols+` FROM messages WHERE folder_id=? AND uid=?`, folderID, int64(uid))
	m, err := scanMessageInfo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageInfo{}, ErrNotFound
	}
	return m, err
}

// MessageUIDByID resolves a message's IMAP UID from its object-store id, reading
// the index. ok is false when the message has no index entry: a calendar or
// contact item is created with CreateMessage and never enters the IMAP index, so
// a poll over such a folder yields an item with no UID (and a (folder, UID)-keyed
// body read is then unavailable — a documented limit for non-mail notification
// targets). A missing row is not an error.
func (s *Store) MessageUIDByID(folderID, messageID int64) (uint32, bool, error) {
	var uid int64
	err := s.idxdb.QueryRow(
		`SELECT uid FROM messages WHERE folder_id=? AND message_id=?`, folderID, messageID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return uint32(uid), true, nil
}

// MessageFlags returns a message's current IMAP flag mask, reporting ErrNotFound
// when no such message exists.
func (s *Store) MessageFlags(folderID int64, uid uint32) (int64, error) {
	var read, replied, flagged, deleted, unsent int
	err := s.idxdb.QueryRow(
		`SELECT read, replied, flagged, deleted, unsent FROM messages WHERE folder_id=? AND uid=?`,
		folderID, int64(uid)).Scan(&read, &replied, &flagged, &deleted, &unsent)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return composeFlags(read, replied, flagged, deleted, unsent), nil
}

// SetMessageFlags replaces a message's IMAP flag mask, identified by folder and
// UID, and mirrors the read state into the object store (the object's own
// read_state, which non-IMAP readers consult). It reports ErrNotFound when no
// such message exists.
func (s *Store) SetMessageFlags(folderID int64, uid uint32, flags int64) error {
	bit := func(f int64) int {
		if flags&f != 0 {
			return 1
		}
		return 0
	}
	var messageID int64
	err := s.idxdb.QueryRow(
		`SELECT message_id FROM messages WHERE folder_id=? AND uid=?`, folderID, int64(uid)).Scan(&messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// The object store is authoritative for read_state and its read_cn, so mirror
	// the read bit there first — allocating a read_cn when it actually flips — before
	// rewriting the IMAP mask. Ordering it first keeps a failed mask update from
	// leaving a read change without the read_cn the ICS download depends on.
	if _, err := s.setObjReadState(messageID, bit(FlagSeen)); err != nil {
		return err
	}
	if _, err := s.idxdb.Exec(
		`UPDATE messages SET read=?, replied=?, flagged=?, deleted=?, unsent=? WHERE message_id=?`,
		bit(FlagSeen), bit(FlagAnswered), bit(FlagFlagged), bit(FlagDeleted), bit(FlagDraft), messageID); err != nil {
		return err
	}
	return nil
}

// SetMessageReadState sets or clears a single message's read flag by its object
// id, touching only the read bit — unlike SetMessageFlags, which replaces the
// whole IMAP flag mask and so would clobber the answered/flagged/deleted bits.
// It mirrors the state into both the index and the object store and reports
// ErrNotFound when no such message exists.
func (s *Store) SetMessageReadState(messageID int64, read bool) error {
	var b int
	if read {
		b = 1
	}
	// Drive existence and the actual-change check from the object store: it holds
	// every message type, while the IMAP index holds only mail, so a non-mail item
	// (a calendar or contact a MAPI client marks read) would otherwise be dropped
	// here and never reach the ICS read-state download.
	found, err := s.setObjReadState(messageID, b)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	// Mirror into the IMAP index. A non-mail message is not indexed, where the
	// zero-row update is a harmless no-op.
	if _, err := s.idxdb.Exec(`UPDATE messages SET read=? WHERE message_id=?`, b, messageID); err != nil {
		return err
	}
	return nil
}

// GetMessageReadState reports whether a message is currently marked read, reading
// the object store's read_state column — the store of record SetMessageReadState
// writes. It reports ErrNotFound when no such message exists. The read-receipt
// trigger reads this before SetMessageReadState so it can fire only on an
// unread→read transition: a message already read through another protocol (an
// IMAP \Seen, say) must not re-fire a receipt when an Outlook client opens it.
func (s *Store) GetMessageReadState(messageID int64) (bool, error) {
	var rs int
	err := s.objdb.QueryRow(`SELECT read_state FROM messages WHERE message_id=?`, messageID).Scan(&rs)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	return rs != 0, nil
}

// setObjReadState mirrors a message's read flag into the object store, allocating a
// fresh read_cn from the mailbox change-number counter only when the state actually
// flips, so the ICS read-state download branch can report the change. The counter
// read and the row update share one transaction, so concurrent flips cannot land on
// the same change number (the read_cn UNIQUE column is the backstop). Folder-
// associated messages carry no read state and are left untouched. It reports whether
// the message exists in the object store — the store of record for every message
// type, mail or not.
func (s *Store) setObjReadState(messageID int64, want int) (found bool, err error) {
	tx, err := s.objdb.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var cur, assoc int
	err = tx.QueryRow(`SELECT read_state, is_associated FROM messages WHERE message_id=?`, messageID).Scan(&cur, &assoc)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if assoc != 0 || cur == want {
		return true, nil // associated (no read state) or already in state — no new read_cn
	}
	rcn, err := allocateCN(tx)
	if err != nil {
		return true, err
	}
	if _, err := tx.Exec(
		`UPDATE messages SET read_state=?, read_cn=? WHERE message_id=?`, want, int64(rcn), messageID); err != nil {
		return true, err
	}
	if err := tx.Commit(); err != nil {
		return true, err
	}
	return true, nil
}
