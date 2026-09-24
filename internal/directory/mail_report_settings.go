package directory

import (
	"database/sql"
	"errors"
	"time"
)

// Default retention windows, in days, applied when no row has been saved. A
// failure report carries the failed message's header block, which is personal
// data, so it is kept for a shorter time than the aggregate counts.
const (
	DefaultMailReportAggregateDays = 180
	DefaultMailReportFailureDays   = 30
)

// MailReportSettings is the operator-editable retention of stored reports, in
// days. AggregateDays covers DMARC aggregate and TLS reports, FailureDays the
// DMARC failure reports. 0 or less keeps reports forever.
type MailReportSettings struct {
	AggregateDays int
	FailureDays   int
}

// GetMailReportSettings returns the stored retention and whether a row has been
// saved. When none has, found is false and the caller uses the defaults.
func (d *SQLDirectory) GetMailReportSettings() (MailReportSettings, bool, error) {
	var s MailReportSettings
	err := d.db.QueryRow(
		`SELECT aggregate_retention_days, failure_retention_days FROM mail_report_settings WHERE id = 1`).
		Scan(&s.AggregateDays, &s.FailureDays)
	if errors.Is(err, sql.ErrNoRows) {
		return MailReportSettings{}, false, nil
	}
	if err != nil {
		return MailReportSettings{}, false, err
	}
	return s, true, nil
}

// SetMailReportSettings persists the retention, upserting the single row so the
// sweep observes the change on its next run.
func (d *SQLDirectory) SetMailReportSettings(s MailReportSettings) error {
	_, err := d.db.Exec(
		`INSERT INTO mail_report_settings (id, aggregate_retention_days, failure_retention_days, updated_at)
		 VALUES (1, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE aggregate_retention_days = VALUES(aggregate_retention_days),
		   failure_retention_days = VALUES(failure_retention_days), updated_at = VALUES(updated_at)`,
		s.AggregateDays, s.FailureDays, time.Now().UnixMilli())
	return err
}

// PruneMailReports deletes the reports that arrived before their retention
// window, reading the window on every call so an operator's change applies
// without a restart. It returns how many reports it deleted; the rows of a
// deleted report go with it.
func (d *SQLDirectory) PruneMailReports(now time.Time) (int64, error) {
	s, found, err := d.GetMailReportSettings()
	if err != nil {
		return 0, err
	}
	if !found {
		s = MailReportSettings{AggregateDays: DefaultMailReportAggregateDays, FailureDays: DefaultMailReportFailureDays}
	}
	var total int64
	for _, p := range []struct {
		table string
		days  int
	}{
		{"dmarc_reports", s.AggregateDays},
		{"tlsrpt_reports", s.AggregateDays},
		{"dmarc_failure_reports", s.FailureDays},
	} {
		n, err := d.pruneReportTable(p.table, p.days, now)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// pruneReportTable deletes one table's reports older than days. A window of 0 or
// less keeps them.
func (d *SQLDirectory) pruneReportTable(table string, days int, now time.Time) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).Unix()
	// #nosec G202 -- table is one of three literal table names from PruneMailReports, never input
	res, err := d.db.Exec(`DELETE FROM `+table+` WHERE received_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
