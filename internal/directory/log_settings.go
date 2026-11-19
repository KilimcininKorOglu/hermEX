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

// DefaultLogSpillLimitMB is the per-daemon spill cap in MiB while no settings row
// has been saved; it matches the column default.
const DefaultLogSpillLimitMB = 1024

// GetLogSpillLimitMB returns the operator-set per-daemon log spill cap in MiB, or
// DefaultLogSpillLimitMB while no row has been saved. Zero means no cap.
func (d *SQLDirectory) GetLogSpillLimitMB() (int, error) {
	var mb int
	err := d.db.QueryRow(`SELECT spill_limit_mb FROM log_settings WHERE id = 1`).Scan(&mb)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultLogSpillLimitMB, nil
	}
	if err != nil {
		return 0, err
	}
	return mb, nil
}

// SetLogSpillLimitMB persists the per-daemon log spill cap in MiB, leaving the
// retention window as it is. Every daemon polls it and applies it without a
// restart. A negative value is refused; zero removes the cap.
func (d *SQLDirectory) SetLogSpillLimitMB(mb int) error {
	if mb < 0 {
		return errors.New("log spill limit must not be negative")
	}
	_, err := d.db.Exec(
		`INSERT INTO log_settings (id, retention_days, spill_limit_mb, updated_at) VALUES (1, 0, ?, ?)
		 ON DUPLICATE KEY UPDATE spill_limit_mb = VALUES(spill_limit_mb), updated_at = VALUES(updated_at)`,
		mb, time.Now().UnixMilli())
	return err
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
