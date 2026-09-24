package directory

import (
	"database/sql"
	"errors"
	"time"
)

// DMARCReportSettings is the operator switch for sending DMARC aggregate reports
// (RFC 7489 §7.2) to the domains whose mail this server receives. The zero value,
// and a missing row, mean off.
type DMARCReportSettings struct {
	Enabled bool
}

// GetDMARCReportSettings returns the stored switch and whether a row has been
// saved. When none has, found is false and reporting stays off.
func (d *SQLDirectory) GetDMARCReportSettings() (DMARCReportSettings, bool, error) {
	var s DMARCReportSettings
	err := d.db.QueryRow(`SELECT enabled FROM dmarc_report_settings WHERE id = 1`).Scan(&s.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return DMARCReportSettings{}, false, nil
	}
	if err != nil {
		return DMARCReportSettings{}, false, err
	}
	return s, true, nil
}

// SetDMARCReportSettings persists the switch, upserting the single row so the
// MTA's poll observes the change on its next read.
func (d *SQLDirectory) SetDMARCReportSettings(s DMARCReportSettings) error {
	_, err := d.db.Exec(
		`INSERT INTO dmarc_report_settings (id, enabled, updated_at) VALUES (1, ?, ?)
		 ON DUPLICATE KEY UPDATE enabled = VALUES(enabled), updated_at = VALUES(updated_at)`,
		s.Enabled, time.Now().UnixMilli())
	return err
}
