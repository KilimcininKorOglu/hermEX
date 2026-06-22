package directory

import (
	"database/sql"
	"errors"
	"time"
)

// GetGreylistEnabled reports whether greylisting is turned on; it defaults to off
// when no row has been saved.
func (d *SQLDirectory) GetGreylistEnabled() (bool, error) {
	var enabled bool
	err := d.db.QueryRow(`SELECT enabled FROM greylist_settings WHERE id = 1`).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return enabled, err
}

// SetGreylistEnabled turns greylisting on or off. It upserts the single row so the
// MTA's poll observes the change and applies it without a restart.
func (d *SQLDirectory) SetGreylistEnabled(on bool) error {
	_, err := d.db.Exec(
		`INSERT INTO greylist_settings (id, enabled, updated_at) VALUES (1, ?, ?)
		 ON DUPLICATE KEY UPDATE enabled = VALUES(enabled), updated_at = VALUES(updated_at)`,
		on, time.Now().UnixMilli())
	return err
}
