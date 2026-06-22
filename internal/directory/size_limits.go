package directory

import (
	"database/sql"
	"errors"
	"time"
)

// SizeLimits is the operator-editable per-protocol request/body size caps, in bytes.
// It is stored as a single row; each protocol daemon polls it and applies its own
// field without a restart. It grows a field per protocol as each daemon is wired.
type SizeLimits struct {
	IMAPLiteralBytes int64
	EWSRequestBytes  int64
}

// GetSizeLimits returns the stored size limits and whether a row has been saved. When
// none has, found is false and each caller keeps its server's built-in default.
func (d *SQLDirectory) GetSizeLimits() (SizeLimits, bool, error) {
	var s SizeLimits
	err := d.db.QueryRow(
		`SELECT imap_literal_bytes, ews_request_bytes FROM size_limits WHERE id = 1`).
		Scan(&s.IMAPLiteralBytes, &s.EWSRequestBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return SizeLimits{}, false, nil
	}
	if err != nil {
		return SizeLimits{}, false, err
	}
	return s, true, nil
}

// SetSizeLimits persists the size limits, upserting the single row so each protocol
// daemon's poll observes the change and applies it without a restart.
func (d *SQLDirectory) SetSizeLimits(s SizeLimits) error {
	_, err := d.db.Exec(
		`INSERT INTO size_limits (id, imap_literal_bytes, ews_request_bytes, updated_at)
		 VALUES (1, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE imap_literal_bytes = VALUES(imap_literal_bytes),
		   ews_request_bytes = VALUES(ews_request_bytes), updated_at = VALUES(updated_at)`,
		s.IMAPLiteralBytes, s.EWSRequestBytes, time.Now().UnixMilli())
	return err
}
