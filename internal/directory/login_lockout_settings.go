package directory

import (
	"database/sql"
	"errors"
	"time"
)

// LoginLockoutSettings is the failed-login limiter's stored tuning: how many
// failures inside the rolling window trip a lockout, the window length in seconds,
// and how long the lockout lasts. It has no on/off toggle, unlike the rate
// limiters: an online brute-force guard is always on, and an operator who needs it
// out of the way raises the threshold.
type LoginLockoutSettings struct {
	MaxFails       int
	WindowSeconds  int
	LockoutSeconds int
}

// GetLoginLockoutSettings returns the stored login-lockout tuning and whether a row
// has been saved. When none has, found is false and the caller keeps the limiter's
// built-in defaults.
func (d *SQLDirectory) GetLoginLockoutSettings() (LoginLockoutSettings, bool, error) {
	var s LoginLockoutSettings
	err := d.db.QueryRow(
		`SELECT max_fails, window_seconds, lockout_seconds FROM login_lockout_settings WHERE id = 1`).
		Scan(&s.MaxFails, &s.WindowSeconds, &s.LockoutSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return LoginLockoutSettings{}, false, nil
	}
	if err != nil {
		return LoginLockoutSettings{}, false, err
	}
	return s, true, nil
}

// SetLoginLockoutSettings persists the login-lockout tuning, upserting the single
// row so every daemon with a login chokepoint observes the change on its next poll
// and applies it without a restart.
func (d *SQLDirectory) SetLoginLockoutSettings(s LoginLockoutSettings) error {
	_, err := d.db.Exec(
		`INSERT INTO login_lockout_settings (id, max_fails, window_seconds, lockout_seconds, updated_at)
		 VALUES (1, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE max_fails = VALUES(max_fails),
		   window_seconds = VALUES(window_seconds), lockout_seconds = VALUES(lockout_seconds),
		   updated_at = VALUES(updated_at)`,
		s.MaxFails, s.WindowSeconds, s.LockoutSeconds, time.Now().UnixMilli())
	return err
}
