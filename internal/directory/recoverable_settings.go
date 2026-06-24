package directory

import (
	"database/sql"
	"errors"
	"time"

	"hermex/internal/objectstore"
)

// DefaultRecoverableRetentionDays is the Exchange-matching retention window applied
// when no row has been saved.
const DefaultRecoverableRetentionDays = 14

// RecoverableSettings is the operator-editable Recoverable Items retention window,
// in days (0 disables auto-purge). It is stored as a single row; a sweep polls it
// and applies a change without a restart.
type RecoverableSettings struct {
	RetentionDays int
}

// GetRecoverableSettings returns the stored retention window and whether a row has
// been saved. When none has, found is false and the caller uses the default.
func (d *SQLDirectory) GetRecoverableSettings() (RecoverableSettings, bool, error) {
	var s RecoverableSettings
	err := d.db.QueryRow(
		`SELECT retention_days FROM recoverable_settings WHERE id = 1`).Scan(&s.RetentionDays)
	if errors.Is(err, sql.ErrNoRows) {
		return RecoverableSettings{}, false, nil
	}
	if err != nil {
		return RecoverableSettings{}, false, err
	}
	return s, true, nil
}

// SetRecoverableSettings persists the retention window, upserting the single row so
// the sweep's poll observes the change and applies it without a restart.
func (d *SQLDirectory) SetRecoverableSettings(s RecoverableSettings) error {
	_, err := d.db.Exec(
		`INSERT INTO recoverable_settings (id, retention_days, updated_at)
		 VALUES (1, ?, ?)
		 ON DUPLICATE KEY UPDATE retention_days = VALUES(retention_days), updated_at = VALUES(updated_at)`,
		s.RetentionDays, time.Now().UnixMilli())
	return err
}

// allMaildirs returns every user's mailbox directory, skipping accounts without one
// (contacts and similar non-login users carry an empty maildir).
func (d *SQLDirectory) allMaildirs() ([]string, error) {
	rows, err := d.db.Query(`SELECT maildir FROM users WHERE maildir <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var md string
		if err := rows.Scan(&md); err != nil {
			return nil, err
		}
		out = append(out, md)
	}
	return out, rows.Err()
}

// SweepRecoverableItems is the retention sweep: it reads the operator-tunable
// retention window (defaulting to DefaultRecoverableRetentionDays when unset) and
// permanently purges every mailbox's soft-deleted items older than that window,
// returning how many it purged. A window of 0 or less disables auto-purge (items are
// kept until manually purged). A mailbox that cannot be opened is skipped, not fatal.
func (d *SQLDirectory) SweepRecoverableItems(now time.Time) (int, error) {
	s, found, err := d.GetRecoverableSettings()
	if err != nil {
		return 0, err
	}
	days := DefaultRecoverableRetentionDays
	if found {
		days = s.RetentionDays
	}
	if days <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	maildirs, err := d.allMaildirs()
	if err != nil {
		return 0, err
	}
	purged := 0
	for _, md := range maildirs {
		st, err := objectstore.Open(md)
		if err != nil {
			continue
		}
		n, err := st.PurgeSoftDeletedOlderThan(cutoff)
		st.Close()
		if err != nil {
			continue
		}
		purged += n
	}
	return purged, nil
}
