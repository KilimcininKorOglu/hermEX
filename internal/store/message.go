package store

import (
	"database/sql"
	"errors"
	"time"
)

// MessageInfo is the per-message metadata IMAP and POP3 need without loading
// the full message body.
type MessageInfo struct {
	ID           int64
	UID          uint32
	InternalDate time.Time
	Size         int64
	Flags        int64
}

// AppendMessage stores a raw RFC822 message in a folder, assigning the next
// IMAP UID atomically with the insert so UIDs stay monotonic and gap-free under
// concurrent appends.
func (s *Store) AppendMessage(folderID int64, raw []byte, internalDate time.Time, flags int64) (MessageInfo, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return MessageInfo{}, err
	}
	defer tx.Rollback()

	var nextUID int64
	if err := tx.QueryRow(`SELECT next_uid FROM folders WHERE folder_id = ?`, folderID).Scan(&nextUID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MessageInfo{}, ErrNotFound
		}
		return MessageInfo{}, err
	}
	if _, err := tx.Exec(`UPDATE folders SET next_uid = next_uid + 1 WHERE folder_id = ?`, folderID); err != nil {
		return MessageInfo{}, err
	}
	unix := internalDate.Unix()
	res, err := tx.Exec(
		`INSERT INTO messages (folder_id, imap_uid, internal_date, rfc822_size, flags, mime)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		folderID, nextUID, unix, int64(len(raw)), flags, raw)
	if err != nil {
		return MessageInfo{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return MessageInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return MessageInfo{}, err
	}
	return MessageInfo{
		ID:           id,
		UID:          uint32(nextUID),
		InternalDate: time.Unix(unix, 0).UTC(),
		Size:         int64(len(raw)),
		Flags:        flags,
	}, nil
}

// ListMessages returns a folder's messages ordered by ascending IMAP UID.
func (s *Store) ListMessages(folderID int64) ([]MessageInfo, error) {
	rows, err := s.db.Query(
		`SELECT message_id, imap_uid, internal_date, rfc822_size, flags
		 FROM messages WHERE folder_id = ? ORDER BY imap_uid`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MessageInfo
	for rows.Next() {
		var m MessageInfo
		var uid, idate int64
		if err := rows.Scan(&m.ID, &uid, &idate, &m.Size, &m.Flags); err != nil {
			return nil, err
		}
		m.UID = uint32(uid)
		m.InternalDate = time.Unix(idate, 0).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// MessageByUID returns a single message's metadata by folder and IMAP UID,
// reporting ErrNotFound when it does not exist.
func (s *Store) MessageByUID(folderID int64, uid uint32) (MessageInfo, error) {
	var m MessageInfo
	var u, idate int64
	err := s.db.QueryRow(
		`SELECT message_id, imap_uid, internal_date, rfc822_size, flags
		 FROM messages WHERE folder_id = ? AND imap_uid = ?`, folderID, int64(uid)).
		Scan(&m.ID, &u, &idate, &m.Size, &m.Flags)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageInfo{}, ErrNotFound
	}
	if err != nil {
		return MessageInfo{}, err
	}
	m.UID = uint32(u)
	m.InternalDate = time.Unix(idate, 0).UTC()
	return m, nil
}

// DeleteMessage removes a message from a folder by its IMAP UID, cascading its
// property bag. It reports ErrNotFound when no such message exists.
func (s *Store) DeleteMessage(folderID int64, uid uint32) error {
	res, err := s.db.Exec(
		`DELETE FROM messages WHERE folder_id = ? AND imap_uid = ?`, folderID, int64(uid))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetMessageRaw returns the raw RFC822 bytes of a message identified by its
// folder and IMAP UID.
func (s *Store) GetMessageRaw(folderID int64, uid uint32) ([]byte, error) {
	var raw []byte
	err := s.db.QueryRow(
		`SELECT mime FROM messages WHERE folder_id = ? AND imap_uid = ?`,
		folderID, int64(uid)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return raw, err
}
