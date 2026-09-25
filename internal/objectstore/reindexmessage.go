package objectstore

import (
	"database/sql"
	"errors"
	"time"
)

// indexState is what an index row knows about a message beyond what its content
// yields: where it is, how it is flagged and when it arrived.
type indexState struct {
	folderID, uid, received, size int64
	flags                         int64
	forwarded                     int
	keywords                      sql.NullString
}

// ReindexMessage gives an edited mail message a new IMAP UID in its folder and
// rewrites its index row from the message's current content. IMAP caches a
// message's envelope and body by UID and never refetches them, so an edit to a
// message an IMAP client may have read is visible there only under a new UID; the
// old UID is recorded as expunged. The message id, the flags, the keywords and the
// arrival time are kept, and the cached wire form and its size are rebuilt. It
// reports ErrNotFound when the message does not exist or is not in the IMAP index,
// which is the case for every non-mail object.
func (s *Store) ReindexMessage(messageID int64) (MessageInfo, error) {
	msg, err := s.OpenMessage(messageID)
	if err != nil {
		return MessageInfo{}, err
	}
	tx, err := s.idxdb.Begin()
	if err != nil {
		return MessageInfo{}, err
	}
	defer tx.Rollback()
	old, err := readIndexState(tx, messageID)
	if err != nil {
		return MessageInfo{}, err
	}
	if err := vanishIndexRow(tx, old.folderID, old.uid, messageID); err != nil {
		return MessageInfo{}, err
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	mid := midString(uint64(messageID))
	uid, err := s.indexMessageTx(tx, old.folderID, messageID, mid, msg, old.size, time.Unix(old.received, 0), old.flags)
	if err != nil {
		return MessageInfo{}, err
	}
	if err := restoreIndexExtras(tx, messageID, old); err != nil {
		return MessageInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return MessageInfo{}, err
	}
	// The row was written with the old size; the rebuild records the size of the
	// bytes the edited message now serves.
	s.refreshEML(messageID)
	s.publishChange("modify", 0, mid)
	// #nosec G115 -- an IMAP UID is a 32-bit counter kept in SQLite's signed 64-bit column
	return s.MessageByUID(old.folderID, uint32(uid))
}

// readIndexState reads a message's index row, reporting ErrNotFound when the
// message has none.
func readIndexState(tx *sql.Tx, messageID int64) (indexState, error) {
	var st indexState
	var read, replied, flagged, deleted, unsent int
	err := tx.QueryRow(
		`SELECT m.folder_id, m.uid, m.received, m.size, m.read, m.replied, m.flagged, m.deleted, m.unsent,
		        m.forwarded, p.flag_string
		 FROM messages m LEFT JOIN mapping p ON p.message_id = m.message_id
		 WHERE m.message_id=?`, messageID).Scan(
		&st.folderID, &st.uid, &st.received, &st.size, &read, &replied, &flagged, &deleted, &unsent,
		&st.forwarded, &st.keywords)
	if errors.Is(err, sql.ErrNoRows) {
		return indexState{}, ErrNotFound
	}
	if err != nil {
		return indexState{}, err
	}
	st.flags = composeFlags(read, replied, flagged, deleted, unsent)
	return st, nil
}

// restoreIndexExtras puts back what indexMessageTx does not take as input: the
// forwarded mark and the IMAP keywords.
func restoreIndexExtras(tx *sql.Tx, messageID int64, old indexState) error {
	if _, err := tx.Exec(`UPDATE messages SET forwarded=? WHERE message_id=?`, old.forwarded, messageID); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE mapping SET flag_string=? WHERE message_id=?`, old.keywords, messageID)
	return err
}
