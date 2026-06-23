package directory

import (
	"database/sql"
	"errors"
	"time"
)

// MTASTSDefaultMaxAge is the policy cache lifetime (seconds) a fresh deployment
// publishes: one day, short enough that a misconfiguration discovered during rollout
// clears from senders' caches quickly. The operator raises it once the policy is
// stable (RFC 8461 allows up to one year).
const MTASTSDefaultMaxAge = 86400

// MTASTSSettings is the operator's choice of whether and how the server publishes an
// MTA-STS policy (RFC 8461) for its own domains. Enabled gates publishing entirely.
// Mode is "testing" (a sender reports TLS failures but still delivers — the safe
// rollout default) or "enforce" (a sender refuses delivery to a non-validated MX);
// "none" withdraws a previously published policy. MaxAge is the policy cache lifetime
// in seconds.
type MTASTSSettings struct {
	Enabled bool
	Mode    string
	MaxAge  int
}

// GetMTASTSSettings returns the stored MTA-STS publishing settings and whether a row
// exists. When none has been saved, found is false and the caller treats it as
// disabled in testing mode — the safe default, so an upgrade never silently starts
// advertising a policy or enforcing TLS on inbound mail.
func (d *SQLDirectory) GetMTASTSSettings() (MTASTSSettings, bool, error) {
	var s MTASTSSettings
	var enabled int
	err := d.db.QueryRow(
		`SELECT enabled, mode, max_age FROM mtasts_settings WHERE id = 1`).
		Scan(&enabled, &s.Mode, &s.MaxAge)
	if errors.Is(err, sql.ErrNoRows) {
		return MTASTSSettings{Enabled: false, Mode: "testing", MaxAge: MTASTSDefaultMaxAge}, false, nil
	}
	if err != nil {
		return MTASTSSettings{}, false, err
	}
	s.Enabled = enabled != 0
	return s, true, nil
}

// SetMTASTSSettings persists the MTA-STS publishing settings, upserting the single
// row. The gateway reads them per request when serving the policy file, so a change
// applies without a restart (a sender adopts it on its next fetch).
func (d *SQLDirectory) SetMTASTSSettings(s MTASTSSettings) error {
	enabled := 0
	if s.Enabled {
		enabled = 1
	}
	_, err := d.db.Exec(
		`INSERT INTO mtasts_settings (id, enabled, mode, max_age, updated_at)
		 VALUES (1, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE enabled = VALUES(enabled), mode = VALUES(mode),
		   max_age = VALUES(max_age), updated_at = VALUES(updated_at)`,
		enabled, s.Mode, s.MaxAge, time.Now().UnixMilli())
	return err
}
