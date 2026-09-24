package directory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"hermex/internal/tlsrpt"
)

// ReportFilter selects the reports one admin may see over one period. All is set
// for a system administrator; otherwise only DomainIDs are visible, and an empty
// set sees nothing. From and To bound the report period in unix seconds, To
// exclusive; a zero To means no upper bound. Limit defaults to 200.
type ReportFilter struct {
	DomainIDs []int64
	All       bool
	From      int64
	To        int64
	Limit     int
}

// ReportListing is one row of a report list. For an aggregate report Total and
// Failed count messages, Failed being those that passed neither DKIM nor SPF; for
// a TLS report they count sessions.
type ReportListing struct {
	ID         int64
	DomainID   int64
	Domain     string
	OrgName    string
	ReportID   string
	Begin      int64
	End        int64
	ReceivedAt int64
	Total      int64
	Failed     int64
}

// scopeArgs returns the arguments of the condition every report list query
// carries, written out in each query so its text never varies:
//
//	(? OR FIND_IN_SET(domain_id, ?) > 0) AND period >= ? AND (? = 0 OR period < ?)
//
// The admin's domain ids travel as one comma-separated argument, and the limit
// follows as the last argument. ok is false when the scope is empty, so the
// caller returns nothing without a query.
func (f ReportFilter) scopeArgs() (args []any, ok bool) {
	if !f.All && len(f.DomainIDs) == 0 {
		return nil, false
	}
	ids := make([]string, len(f.DomainIDs))
	for i, id := range f.DomainIDs {
		ids[i] = strconv.FormatInt(id, 10)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	return []any{f.All, strings.Join(ids, ","), f.From, f.To, f.To, limit}, true
}

// ListDMARCReports lists aggregate reports, newest period first.
func (d *SQLDirectory) ListDMARCReports(f ReportFilter) ([]ReportListing, error) {
	args, ok := f.scopeArgs()
	if !ok {
		return nil, nil
	}
	return d.queryListings(
		`SELECT p.id, p.domain_id, COALESCE(dm.domainname, ''), p.org_name, p.report_id, p.date_begin, p.date_end,
		   p.received_at, COALESCE(SUM(r.msg_count), 0),
		   COALESCE(SUM(CASE WHEN r.dkim_eval <> 'pass' AND r.spf_eval <> 'pass' THEN r.msg_count ELSE 0 END), 0)
		 FROM dmarc_reports p
		 LEFT JOIN domains dm ON dm.id = p.domain_id
		 LEFT JOIN dmarc_report_records r ON r.dmarc_report_id = p.id
		 WHERE (? OR FIND_IN_SET(p.domain_id, ?) > 0) AND p.date_begin >= ? AND (? = 0 OR p.date_begin < ?)
		 GROUP BY p.id ORDER BY p.date_begin DESC, p.id DESC LIMIT ?`, args)
}

// ListTLSReports lists TLS reports, newest period first.
func (d *SQLDirectory) ListTLSReports(f ReportFilter) ([]ReportListing, error) {
	args, ok := f.scopeArgs()
	if !ok {
		return nil, nil
	}
	return d.queryListings(
		`SELECT p.id, p.domain_id, COALESCE(dm.domainname, ''), p.org_name, p.report_id, p.date_begin, p.date_end,
		   p.received_at, COALESCE(SUM(t.success_count + t.failure_count), 0), COALESCE(SUM(t.failure_count), 0)
		 FROM tlsrpt_reports p
		 LEFT JOIN domains dm ON dm.id = p.domain_id
		 LEFT JOIN tlsrpt_report_policies t ON t.tlsrpt_report_id = p.id
		 WHERE (? OR FIND_IN_SET(p.domain_id, ?) > 0) AND p.date_begin >= ? AND (? = 0 OR p.date_begin < ?)
		 GROUP BY p.id ORDER BY p.date_begin DESC, p.id DESC LIMIT ?`, args)
}

func (d *SQLDirectory) queryListings(q string, args []any) ([]ReportListing, error) {
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReportListing
	for rows.Next() {
		var l ReportListing
		if err := rows.Scan(&l.ID, &l.DomainID, &l.Domain, &l.OrgName, &l.ReportID, &l.Begin, &l.End,
			&l.ReceivedAt, &l.Total, &l.Failed); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DMARCReport is one aggregate report with its records, for the detail page.
type DMARCReport struct {
	ReportListing
	ReporterEmail string
	PolicyP       string
	PolicySP      string
	PolicyPct     string
	ADKIM         string
	ASPF          string
	MailFrom      string
	RemoteAddr    string
	Records       []DMARCRecordRow
}

// DMARCRecordRow is one stored aggregate record.
type DMARCRecordRow struct {
	SourceIP     string
	Count        int64
	Disposition  string
	DKIM         string
	SPF          string
	HeaderFrom   string
	EnvelopeFrom string
	DKIMResults  string
	SPFResults   string
}

// GetDMARCReport returns one aggregate report (ok=false when absent). The caller
// checks DomainID against the admin's scope before showing it.
func (d *SQLDirectory) GetDMARCReport(id int64) (DMARCReport, bool, error) {
	var r DMARCReport
	err := d.db.QueryRow(
		`SELECT p.id, p.domain_id, COALESCE(dm.domainname, ''), p.org_name, p.report_id, p.date_begin, p.date_end,
		   p.received_at, p.reporter_email, p.policy_p, p.policy_sp, p.policy_pct, p.adkim, p.aspf, p.mail_from, p.remote_addr
		 FROM dmarc_reports p LEFT JOIN domains dm ON dm.id = p.domain_id WHERE p.id = ?`, id).Scan(
		&r.ID, &r.DomainID, &r.Domain, &r.OrgName, &r.ReportID, &r.Begin, &r.End, &r.ReceivedAt,
		&r.ReporterEmail, &r.PolicyP, &r.PolicySP, &r.PolicyPct, &r.ADKIM, &r.ASPF, &r.MailFrom, &r.RemoteAddr)
	if errors.Is(err, sql.ErrNoRows) {
		return DMARCReport{}, false, nil
	}
	if err != nil {
		return DMARCReport{}, false, err
	}
	r.Records, err = d.dmarcRecords(id)
	if err != nil {
		return DMARCReport{}, false, err
	}
	for _, rec := range r.Records {
		r.Total += rec.Count
		if rec.DKIM != "pass" && rec.SPF != "pass" {
			r.Failed += rec.Count
		}
	}
	return r, true, nil
}

func (d *SQLDirectory) dmarcRecords(reportID int64) ([]DMARCRecordRow, error) {
	rows, err := d.db.Query(
		`SELECT source_ip, msg_count, disposition, dkim_eval, spf_eval, header_from, envelope_from, dkim_results, spf_results
		 FROM dmarc_report_records WHERE dmarc_report_id = ? ORDER BY msg_count DESC, id`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DMARCRecordRow
	for rows.Next() {
		var r DMARCRecordRow
		if err := rows.Scan(&r.SourceIP, &r.Count, &r.Disposition, &r.DKIM, &r.SPF, &r.HeaderFrom,
			&r.EnvelopeFrom, &r.DKIMResults, &r.SPFResults); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TLSReport is one TLS report with its policies, for the detail page.
type TLSReport struct {
	ReportListing
	Contact    string
	MailFrom   string
	RemoteAddr string
	Policies   []TLSPolicyRow
}

// TLSPolicyRow is one stored policy block of a TLS report.
type TLSPolicyRow struct {
	PolicyType     string
	PolicyDomain   string
	MXHost         string
	Success        int64
	Failure        int64
	FailureDetails []tlsrpt.FailureDetail
}

// GetTLSReport returns one TLS report (ok=false when absent). The caller checks
// DomainID against the admin's scope before showing it.
func (d *SQLDirectory) GetTLSReport(id int64) (TLSReport, bool, error) {
	var r TLSReport
	err := d.db.QueryRow(
		`SELECT p.id, p.domain_id, COALESCE(dm.domainname, ''), p.org_name, p.report_id, p.date_begin, p.date_end,
		   p.received_at, p.contact, p.mail_from, p.remote_addr
		 FROM tlsrpt_reports p LEFT JOIN domains dm ON dm.id = p.domain_id WHERE p.id = ?`, id).Scan(
		&r.ID, &r.DomainID, &r.Domain, &r.OrgName, &r.ReportID, &r.Begin, &r.End, &r.ReceivedAt,
		&r.Contact, &r.MailFrom, &r.RemoteAddr)
	if errors.Is(err, sql.ErrNoRows) {
		return TLSReport{}, false, nil
	}
	if err != nil {
		return TLSReport{}, false, err
	}
	r.Policies, err = d.tlsPolicies(id)
	if err != nil {
		return TLSReport{}, false, err
	}
	for _, p := range r.Policies {
		r.Total += p.Success + p.Failure
		r.Failed += p.Failure
	}
	return r, true, nil
}

func (d *SQLDirectory) tlsPolicies(reportID int64) ([]TLSPolicyRow, error) {
	rows, err := d.db.Query(
		`SELECT policy_type, policy_domain, mx_host, success_count, failure_count, failure_details
		 FROM tlsrpt_report_policies WHERE tlsrpt_report_id = ? ORDER BY id`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TLSPolicyRow
	for rows.Next() {
		var p TLSPolicyRow
		var details string
		if err := rows.Scan(&p.PolicyType, &p.PolicyDomain, &p.MXHost, &p.Success, &p.Failure, &details); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(details), &p.FailureDetails); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DMARCFailure is one stored failure report.
type DMARCFailure struct {
	ID                    int64
	DomainID              int64
	Domain                string
	ReceivedAt            int64
	ArrivalDate           int64
	SourceIP              string
	AuthFailure           string
	OriginalMailFrom      string
	OriginalRcptTo        string
	DKIMDomain            string
	DeliveryResult        string
	AuthenticationResults string
	OriginalHeaders       string
	MailFrom              string
	RemoteAddr            string
}

func scanFailure(s rowScanner) (DMARCFailure, error) {
	var f DMARCFailure
	err := s.Scan(&f.ID, &f.DomainID, &f.Domain, &f.ReceivedAt, &f.ArrivalDate, &f.SourceIP, &f.AuthFailure,
		&f.OriginalMailFrom, &f.OriginalRcptTo, &f.DKIMDomain, &f.DeliveryResult, &f.AuthenticationResults,
		&f.OriginalHeaders, &f.MailFrom, &f.RemoteAddr)
	return f, err
}

// ListDMARCFailures lists failure reports, newest first. The period is matched on
// the time the report arrived here, because a reporter may omit the failed
// message's arrival date.
func (d *SQLDirectory) ListDMARCFailures(f ReportFilter) ([]DMARCFailure, error) {
	args, ok := f.scopeArgs()
	if !ok {
		return nil, nil
	}
	rows, err := d.db.Query(
		`SELECT f.id, f.domain_id, COALESCE(dm.domainname, ''), f.received_at, f.arrival_date, f.source_ip,
		   f.auth_failure, f.original_mail_from, f.original_rcpt_to, f.dkim_domain, f.delivery_result,
		   f.authentication_results, f.original_headers, f.mail_from, f.remote_addr
		 FROM dmarc_failure_reports f
		 LEFT JOIN domains dm ON dm.id = f.domain_id
		 WHERE (? OR FIND_IN_SET(f.domain_id, ?) > 0) AND f.received_at >= ? AND (? = 0 OR f.received_at < ?)
		 ORDER BY f.received_at DESC, f.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DMARCFailure
	for rows.Next() {
		rec, err := scanFailure(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// GetDMARCFailure returns one failure report (ok=false when absent). The caller
// checks DomainID against the admin's scope before showing it.
func (d *SQLDirectory) GetDMARCFailure(id int64) (DMARCFailure, bool, error) {
	rec, err := scanFailure(d.db.QueryRow(
		`SELECT f.id, f.domain_id, COALESCE(dm.domainname, ''), f.received_at, f.arrival_date, f.source_ip,
		   f.auth_failure, f.original_mail_from, f.original_rcpt_to, f.dkim_domain, f.delivery_result,
		   f.authentication_results, f.original_headers, f.mail_from, f.remote_addr
		 FROM dmarc_failure_reports f
		 LEFT JOIN domains dm ON dm.id = f.domain_id WHERE f.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return DMARCFailure{}, false, nil
	}
	if err != nil {
		return DMARCFailure{}, false, err
	}
	return rec, true, nil
}
