package objectstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// MessageChangeNumber returns a message's current change number, which every
// write that changes the message advances. A surface that versions an item, such
// as an EWS change key, reads it here, so the version moves on each edit while
// the message id stays. It returns ErrNotFound when no message has the id.
func (s *Store) MessageChangeNumber(messageID int64) (uint64, error) {
	var cn int64
	err := s.objdb.QueryRow(`SELECT change_number FROM messages WHERE message_id=?`, messageID).Scan(&cn)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("objectstore: read message %d change number: %w", messageID, err)
	}
	// #nosec G115 -- a change number crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	return uint64(cn), nil
}
