package directory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"hermex/internal/mailreport"
	"hermex/internal/tlsrpt"
)

// ErrDuplicateReport means the report is already stored: a reporter resends a
// report it is not sure arrived, and the second copy adds nothing.
var ErrDuplicateReport = errors.New("directory: the report is already stored")

// ReportSource is where a stored report came from. DomainID is the hosted domain
// the report speaks for, which scopes it in the admin panel; MailFrom and
// RemoteAddr record who delivered it, because a report is the sender's claim and
// nothing more.
type ReportSource struct {
	DomainID   int64
	ReceivedAt int64
	MailFrom   string
	RemoteAddr string
}

// StoreDMARCAggregate stores an aggregate report and its records in one
// transaction and returns the report's id.
func (d *SQLDirectory) StoreDMARCAggregate(src ReportSource, a *mailreport.Aggregate) (int64, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := refuseDuplicate(tx, "dmarc_reports", src.DomainID, a.OrgName, a.ReportID); err != nil {
		return 0, err
	}
	res, err := tx.Exec(
		`INSERT INTO dmarc_reports (domain_id, org_name, report_id, reporter_email, date_begin, date_end,
		   policy_p, policy_sp, policy_pct, adkim, aspf, received_at, mail_from, remote_addr)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		src.DomainID, capString(a.OrgName, 255), capString(a.ReportID, 255), capString(a.Email, 320),
		a.Begin.Unix(), a.End.Unix(), capString(a.P, 16), capString(a.SP, 16), capString(a.Pct, 8),
		capString(a.ADKIM, 8), capString(a.ASPF, 8), src.ReceivedAt, capString(src.MailFrom, 320), capString(src.RemoteAddr, 64))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := insertDMARCRecords(tx, id, a.Records); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// insertDMARCRecords writes one row per aggregate record.
func insertDMARCRecords(tx *sql.Tx, reportID int64, records []mailreport.Record) error {
	stmt, err := tx.Prepare(
		`INSERT INTO dmarc_report_records (dmarc_report_id, source_ip, msg_count, disposition, dkim_eval, spf_eval,
		   header_from, envelope_from, dkim_results, spf_results)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range records {
		if _, err := stmt.Exec(reportID, capString(r.SourceIP, 45), r.Count, capString(r.Disposition, 16),
			capString(r.DKIM, 16), capString(r.SPF, 16), capString(r.HeaderFrom, 255), capString(r.EnvelopeFrom, 255),
			formatAuthResults(r.DKIMResults), formatAuthResults(r.SPFResults)); err != nil {
			return err
		}
	}
	return nil
}

// formatAuthResults joins raw authentication results into one readable line, the
// form the panel shows them in: "domain/selector=result; ...".
func formatAuthResults(rs []mailreport.AuthResult) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		name := r.Domain
		if r.Selector != "" {
			name += "/" + r.Selector
		}
		parts = append(parts, name+"="+r.Result)
	}
	return capString(strings.Join(parts, "; "), 4096)
}

// StoreTLSReport stores a TLS report and its policies in one transaction and
// returns the report's id.
func (d *SQLDirectory) StoreTLSReport(src ReportSource, r *tlsrpt.Report) (int64, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := refuseDuplicate(tx, "tlsrpt_reports", src.DomainID, r.OrganizationName, r.ReportID); err != nil {
		return 0, err
	}
	res, err := tx.Exec(
		`INSERT INTO tlsrpt_reports (domain_id, org_name, report_id, contact, date_begin, date_end,
		   received_at, mail_from, remote_addr)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		src.DomainID, capString(r.OrganizationName, 255), capString(r.ReportID, 255), capString(r.ContactInfo, 320),
		r.DateRange.Start.Unix(), r.DateRange.End.Unix(), src.ReceivedAt, capString(src.MailFrom, 320), capString(src.RemoteAddr, 64))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := insertTLSPolicies(tx, id, r.Policies); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// insertTLSPolicies writes one row per evaluated policy. The failure details keep
// their RFC 8460 JSON form, because the panel shows them as the reporter sent them.
func insertTLSPolicies(tx *sql.Tx, reportID int64, policies []tlsrpt.PolicyResult) error {
	stmt, err := tx.Prepare(
		`INSERT INTO tlsrpt_report_policies (tlsrpt_report_id, policy_type, policy_domain, mx_host,
		   success_count, failure_count, failure_details)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range policies {
		details, err := json.Marshal(p.FailureDetails)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(reportID, capString(p.Policy.PolicyType, 32), capString(p.Policy.PolicyDomain, 255),
			capString(p.Policy.MXHost, 255), p.Summary.TotalSuccessful, p.Summary.TotalFailure, string(details)); err != nil {
			return err
		}
	}
	return nil
}

// StoreDMARCFailure stores one failure report and returns its id. A failure report
// names no report id, so it is never a duplicate.
func (d *SQLDirectory) StoreDMARCFailure(src ReportSource, f *mailreport.Failure) (int64, error) {
	var arrival int64
	if !f.ArrivalDate.IsZero() {
		arrival = f.ArrivalDate.Unix()
	}
	res, err := d.db.Exec(
		`INSERT INTO dmarc_failure_reports (domain_id, received_at, arrival_date, source_ip, auth_failure,
		   original_mail_from, original_rcpt_to, dkim_domain, delivery_result, authentication_results,
		   original_headers, mail_from, remote_addr)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		src.DomainID, src.ReceivedAt, arrival, capString(f.SourceIP, 64), capString(f.AuthFailure, 64),
		capString(f.OriginalMailFrom, 320), capString(f.OriginalRcptTo, 4096), capString(f.DKIMDomain, 255),
		capString(f.DeliveryResult, 64), capString(f.AuthenticationResults, 8192), f.OriginalHeaders,
		capString(src.MailFrom, 320), capString(src.RemoteAddr, 64))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// refuseDuplicate reports ErrDuplicateReport when the domain already holds this
// reporter's report. The row lock keeps a concurrent copy of the same report from
// passing the check too; a copy that races past it still hits the unique key.
func refuseDuplicate(tx *sql.Tx, table string, domainID int64, orgName, reportID string) error {
	var id int64
	// #nosec G202 -- table is one of two literal table names chosen by the caller, never input
	err := tx.QueryRow(`SELECT id FROM `+table+` WHERE domain_id = ? AND org_name = ? AND report_id = ? FOR UPDATE`,
		domainID, capString(orgName, 255), capString(reportID, 255)).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	return ErrDuplicateReport
}
