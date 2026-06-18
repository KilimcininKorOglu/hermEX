package objectstore

import (
	"database/sql"
	"errors"

	"hermex/internal/mapi"
)

// CreateAttachment inserts a new, empty attachment under a message and assigns it
// a stable attach number — the per-message PidTagAttachNumber the client reopens
// and deletes it by. The number is the message's current maximum plus one (the
// first is 0), computed and persisted in the same transaction as the insert so
// two creates before any other write get distinct numbers without a separate
// in-memory counter. Unlike a row's ordinal position, this number never shifts
// when a sibling attachment is deleted, so a handle the client holds stays valid.
// initialProps are the attachment's opening properties (rendering position,
// timestamps); the caller fills the payload and filename later via
// SaveChangesAttachment. It does not bump the parent message's change number —
// the reference advances that only on the message's own save, so an attachment
// change is observed by ICS only once the message is saved.
func (s *Store) CreateAttachment(messageID int64, initialProps mapi.PropertyValues) (attachmentID int64, attachNum uint32, err error) {
	tx, err := s.objdb.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM messages WHERE message_id=?`, messageID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, ErrNotFound
		}
		return 0, 0, err
	}

	var next int64
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(ap.propval), -1) + 1
		   FROM attachments a
		   JOIN attachment_properties ap ON ap.attachment_id = a.attachment_id
		  WHERE a.message_id = ? AND ap.proptag = ?`,
		messageID, int64(uint32(mapi.PrAttachNum))).Scan(&next); err != nil {
		return 0, 0, err
	}

	res, err := tx.Exec(`INSERT INTO attachments (message_id) VALUES (?)`, messageID)
	if err != nil {
		return 0, 0, err
	}
	aid, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}

	props := append(mapi.PropertyValues(nil), initialProps...)
	props.Set(mapi.PrAttachNum, int32(next))
	if err := s.insertProps(tx, "attachment_properties", "attachment_id", aid, props); err != nil {
		return 0, 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return aid, uint32(next), nil
}

// DeleteAttachment removes the attachment a message holds at a given attach
// number (PidTagAttachNumber), cascading its property rows. It resolves the
// number to a stored attachment row rather than a position, so deleting one
// attachment never renumbers the others. It reports ErrNotFound when the message
// has no attachment at that number, which the ROP layer surfaces as
// MAPI_E_NOT_FOUND. Like CreateAttachment, it leaves the parent message's change
// number untouched.
func (s *Store) DeleteAttachment(messageID int64, attachNum uint32) error {
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var aid int64
	err = tx.QueryRow(
		`SELECT a.attachment_id
		   FROM attachments a
		   JOIN attachment_properties ap ON ap.attachment_id = a.attachment_id
		  WHERE a.message_id = ? AND ap.proptag = ? AND ap.propval = ?`,
		messageID, int64(uint32(mapi.PrAttachNum)), int64(attachNum)).Scan(&aid)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM attachments WHERE attachment_id=?`, aid); err != nil {
		return err
	}
	return tx.Commit()
}
