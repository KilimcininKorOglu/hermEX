package directory

import (
	"database/sql"
	"errors"
	"time"
)

// RelaySettings is the operator-editable outbound delivery retry policy: the base
// backoff in seconds before the first retry, and the number of attempts before a
// recipient is abandoned. It is stored as a single row; the MTA polls it and applies a
// change without a restart.
type RelaySettings struct {
	BackoffSeconds int
	MaxAttempts    int
}

// GetRelaySettings returns the stored relay settings and whether a row has been saved.
// When none has, found is false and the caller keeps the relay worker's built-in
// defaults.
func (d *SQLDirectory) GetRelaySettings() (RelaySettings, bool, error) {
	var s RelaySettings
	err := d.db.QueryRow(
		`SELECT backoff_seconds, max_attempts FROM relay_settings WHERE id = 1`).
		Scan(&s.BackoffSeconds, &s.MaxAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return RelaySettings{}, false, nil
	}
	if err != nil {
		return RelaySettings{}, false, err
	}
	return s, true, nil
}

// SetRelaySettings persists the relay settings, upserting the single row so the MTA's
// poll observes the change and applies it without a restart.
func (d *SQLDirectory) SetRelaySettings(s RelaySettings) error {
	_, err := d.db.Exec(
		`INSERT INTO relay_settings (id, backoff_seconds, max_attempts, updated_at)
		 VALUES (1, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE backoff_seconds = VALUES(backoff_seconds),
		   max_attempts = VALUES(max_attempts), updated_at = VALUES(updated_at)`,
		s.BackoffSeconds, s.MaxAttempts, time.Now().UnixMilli())
	return err
}
