package directory

import (
	"database/sql"
	"errors"
	"time"
)

// ConnLimitSettings is the concurrent-connection cap's stored configuration: its
// on/off toggle, how many connections one daemon serves at once, and how many one
// client address may hold. It is deliberately separate from HTTPRateLimitSettings,
// which counts HTTP requests per window: this one bounds concurrency on the
// connection-oriented protocols and an operator tunes it independently.
type ConnLimitSettings struct {
	Enabled      bool
	MaxTotal     int
	MaxPerClient int
}

// GetConnLimitSettings returns the stored connection-cap settings and whether a
// row has been saved. When none has, found is false and the caller keeps the
// limiter's built-in defaults (disabled).
func (d *SQLDirectory) GetConnLimitSettings() (ConnLimitSettings, bool, error) {
	var s ConnLimitSettings
	err := d.db.QueryRow(
		`SELECT enabled, max_total, max_per_client FROM conn_limit_settings WHERE id = 1`).
		Scan(&s.Enabled, &s.MaxTotal, &s.MaxPerClient)
	if errors.Is(err, sql.ErrNoRows) {
		return ConnLimitSettings{}, false, nil
	}
	if err != nil {
		return ConnLimitSettings{}, false, err
	}
	return s, true, nil
}

// SetConnLimitSettings persists the connection-cap settings, upserting the single
// row so every connection-oriented daemon's poll observes the change and applies
// it without a restart.
func (d *SQLDirectory) SetConnLimitSettings(s ConnLimitSettings) error {
	_, err := d.db.Exec(
		`INSERT INTO conn_limit_settings (id, enabled, max_total, max_per_client, updated_at)
		 VALUES (1, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE enabled = VALUES(enabled), max_total = VALUES(max_total),
		   max_per_client = VALUES(max_per_client), updated_at = VALUES(updated_at)`,
		s.Enabled, s.MaxTotal, s.MaxPerClient, time.Now().UnixMilli())
	return err
}
