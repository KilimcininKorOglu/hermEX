package directory

import (
	"database/sql"
	"errors"
	"time"
)

// RateLimitSettings is the inbound per-IP rate limiter's stored configuration: its
// on/off toggle, the message burst admitted per window, and the window length in
// seconds.
type RateLimitSettings struct {
	Enabled       bool
	Burst         int
	WindowSeconds int
}

// GetRateLimitSettings returns the stored rate-limit settings and whether a row has
// been saved. When none has, found is false and the caller keeps the limiter's
// built-in defaults (disabled).
func (d *SQLDirectory) GetRateLimitSettings() (RateLimitSettings, bool, error) {
	var s RateLimitSettings
	err := d.db.QueryRow(
		`SELECT enabled, burst, window_seconds FROM rate_limit_settings WHERE id = 1`).
		Scan(&s.Enabled, &s.Burst, &s.WindowSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return RateLimitSettings{}, false, nil
	}
	if err != nil {
		return RateLimitSettings{}, false, err
	}
	return s, true, nil
}

// SetRateLimitSettings persists the rate-limit settings, upserting the single row so
// the MTA's poll observes the change and applies it without a restart.
func (d *SQLDirectory) SetRateLimitSettings(s RateLimitSettings) error {
	_, err := d.db.Exec(
		`INSERT INTO rate_limit_settings (id, enabled, burst, window_seconds, updated_at)
		 VALUES (1, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE enabled = VALUES(enabled), burst = VALUES(burst),
		   window_seconds = VALUES(window_seconds), updated_at = VALUES(updated_at)`,
		s.Enabled, s.Burst, s.WindowSeconds, time.Now().UnixMilli())
	return err
}
