package directory

import (
	"database/sql"
	"errors"
	"time"
)

// HTTPRateLimitSettings is the per-client HTTP request limiter's stored
// configuration: its on/off toggle, the request burst admitted per window, and
// the window length in seconds. It is deliberately separate from
// RateLimitSettings, which configures the inbound-SMTP limiter: the two govern
// different protocols and an operator tunes them independently.
type HTTPRateLimitSettings struct {
	Enabled       bool
	Burst         int
	WindowSeconds int
}

// GetHTTPRateLimitSettings returns the stored HTTP rate-limit settings and
// whether a row has been saved. When none has, found is false and the caller
// keeps the limiter's built-in defaults (disabled).
func (d *SQLDirectory) GetHTTPRateLimitSettings() (HTTPRateLimitSettings, bool, error) {
	var s HTTPRateLimitSettings
	err := d.db.QueryRow(
		`SELECT enabled, burst, window_seconds FROM http_rate_limit_settings WHERE id = 1`).
		Scan(&s.Enabled, &s.Burst, &s.WindowSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return HTTPRateLimitSettings{}, false, nil
	}
	if err != nil {
		return HTTPRateLimitSettings{}, false, err
	}
	return s, true, nil
}

// SetHTTPRateLimitSettings persists the HTTP rate-limit settings, upserting the
// single row so every HTTP daemon's poll observes the change and applies it
// without a restart.
func (d *SQLDirectory) SetHTTPRateLimitSettings(s HTTPRateLimitSettings) error {
	_, err := d.db.Exec(
		`INSERT INTO http_rate_limit_settings (id, enabled, burst, window_seconds, updated_at)
		 VALUES (1, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE enabled = VALUES(enabled), burst = VALUES(burst),
		   window_seconds = VALUES(window_seconds), updated_at = VALUES(updated_at)`,
		s.Enabled, s.Burst, s.WindowSeconds, time.Now().UnixMilli())
	return err
}
