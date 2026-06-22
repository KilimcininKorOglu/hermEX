package directory

import (
	"database/sql"
	"errors"
	"time"
)

// OutboundSettings is the outbound abuse limiter's stored configuration: its on/off
// toggle, the external-recipient cap per window, and the window length in seconds.
type OutboundSettings struct {
	Enabled       bool
	RecipientCap  int
	WindowSeconds int
}

// GetOutboundSettings returns the stored outbound-abuse settings and whether a row
// has been saved. When none has, found is false and the caller keeps the limiter's
// built-in defaults (disabled).
func (d *SQLDirectory) GetOutboundSettings() (OutboundSettings, bool, error) {
	var s OutboundSettings
	err := d.db.QueryRow(
		`SELECT enabled, recipient_cap, window_seconds FROM outbound_settings WHERE id = 1`).
		Scan(&s.Enabled, &s.RecipientCap, &s.WindowSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return OutboundSettings{}, false, nil
	}
	if err != nil {
		return OutboundSettings{}, false, err
	}
	return s, true, nil
}

// SetOutboundSettings persists the outbound-abuse settings, upserting the single row
// so the MTA's poll observes the change and applies it without a restart.
func (d *SQLDirectory) SetOutboundSettings(s OutboundSettings) error {
	_, err := d.db.Exec(
		`INSERT INTO outbound_settings (id, enabled, recipient_cap, window_seconds, updated_at)
		 VALUES (1, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE enabled = VALUES(enabled), recipient_cap = VALUES(recipient_cap),
		   window_seconds = VALUES(window_seconds), updated_at = VALUES(updated_at)`,
		s.Enabled, s.RecipientCap, s.WindowSeconds, time.Now().UnixMilli())
	return err
}
