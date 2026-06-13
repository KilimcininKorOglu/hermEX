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
	if _, err := s.idxdb.Exec(
		`UPDATE messages SET read=?, replied=?, flagged=?, deleted=?, unsent=? WHERE message_id=?`,
		bit(FlagSeen), bit(FlagAnswered), bit(FlagFlagged), bit(FlagDeleted), bit(FlagDraft), messageID); err != nil {
		return err
	}
	if _, err := s.objdb.Exec(
		`UPDATE messages SET read_state=? WHERE message_id=?`, bit(FlagSeen), messageID); err != nil {
		return err
	}
	return nil
}
