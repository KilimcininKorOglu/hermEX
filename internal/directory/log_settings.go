package directory

import (
	"database/sql"
	"errors"
	"time"
)

// GetLogRetentionDays returns the operator-set central-log retention window in days and
// whether a row has been saved. When none has, found is false and the admin keeps its
// seed (the config value). Zero or negative means keep logs forever.
func (d *SQLDirectory) GetLogRetentionDays() (int, bool, error) {
	var days int
	err := d.db.QueryRow(`SELECT retention_days FROM log_settings WHERE id = 1`).Scan(&days)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return days, true, nil
}

// SetLogRetentionDays persists the central-log retention window (in days), upserting the
// single row. The admin daemon polls it and prunes the log store to match without a
// restart; zero or negative keeps logs forever, so nothing is pruned.
func (d *SQLDirectory) SetLogRetentionDays(days int) error {
	_, err := d.db.Exec(
		`INSERT INTO log_settings (id, retention_days, updated_at) VALUES (1, ?, ?)
		 ON DUPLICATE KEY UPDATE retention_days = VALUES(retention_days), updated_at = VALUES(updated_at)`,
		days, time.Now().UnixMilli())
	return err
}
