package directory

import (
	"database/sql"
	"errors"
	"time"
)

// SpamHistorySettings is the operator-editable retention bound for the spam-history
// table: how many of the most recent scored verdicts to keep. It is stored as a
// single row; the MTA polls it and applies a change without a restart.
type SpamHistorySettings struct {
	Retain int
}

// GetSpamHistorySettings returns the stored retention setting and whether a row has
// been saved. When none has, found is false and the caller keeps the built-in
// default (defaultSpamHistoryRetain).
func (d *SQLDirectory) GetSpamHistorySettings() (SpamHistorySettings, bool, error) {
	var s SpamHistorySettings
	err := d.db.QueryRow(
		`SELECT retain_count FROM spam_history_settings WHERE id = 1`).Scan(&s.Retain)
	if errors.Is(err, sql.ErrNoRows) {
		return SpamHistorySettings{}, false, nil
	}
	if err != nil {
		return SpamHistorySettings{}, false, err
	}
	return s, true, nil
}

// SetSpamHistorySettings persists the retention setting, upserting the single row so
// the MTA's poll observes the change and applies it without a restart.
func (d *SQLDirectory) SetSpamHistorySettings(s SpamHistorySettings) error {
	_, err := d.db.Exec(
		`INSERT INTO spam_history_settings (id, retain_count, updated_at)
		 VALUES (1, ?, ?)
		 ON DUPLICATE KEY UPDATE retain_count = VALUES(retain_count), updated_at = VALUES(updated_at)`,
		s.Retain, time.Now().UnixMilli())
	return err
}
