package directory

import (
	"database/sql"
	"errors"
	"time"
)

// MessageSizeSettings is the operator-editable inbound SMTP message size limit, in
// bytes (0 means no limit). It is stored as a single row; the MTA polls it and applies
// a change without a restart.
type MessageSizeSettings struct {
	MaxInboundBytes int64
}

// GetMessageSizeSettings returns the stored message size limit and whether a row has
// been saved. When none has, found is false and the caller keeps the server's built-in
// behavior (no limit).
func (d *SQLDirectory) GetMessageSizeSettings() (MessageSizeSettings, bool, error) {
	var s MessageSizeSettings
	err := d.db.QueryRow(
		`SELECT max_inbound_bytes FROM message_size_settings WHERE id = 1`).Scan(&s.MaxInboundBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageSizeSettings{}, false, nil
	}
	if err != nil {
		return MessageSizeSettings{}, false, err
	}
	return s, true, nil
}

// SetMessageSizeSettings persists the message size limit, upserting the single row so
// the MTA's poll observes the change and applies it without a restart.
func (d *SQLDirectory) SetMessageSizeSettings(s MessageSizeSettings) error {
	_, err := d.db.Exec(
		`INSERT INTO message_size_settings (id, max_inbound_bytes, updated_at)
		 VALUES (1, ?, ?)
		 ON DUPLICATE KEY UPDATE max_inbound_bytes = VALUES(max_inbound_bytes), updated_at = VALUES(updated_at)`,
		s.MaxInboundBytes, time.Now().UnixMilli())
	return err
}
