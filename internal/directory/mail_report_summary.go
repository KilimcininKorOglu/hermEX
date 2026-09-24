package directory

// DMARCSourceSummary totals the aggregate records of one sending source for one
// From domain across every report in the filter's period. DMARCPass counts the
// messages that passed DKIM or SPF, which is what a DMARC pass requires.
type DMARCSourceSummary struct {
	Domain      string
	HeaderFrom  string
	SourceIP    string
	Messages    int64
	DKIMPass    int64
	SPFPass     int64
	DMARCPass   int64
	Quarantined int64
	Rejected    int64
}

// DMARCSummary totals aggregate records by domain, From domain and source
// address, the sources that sent the most messages first.
func (d *SQLDirectory) DMARCSummary(f ReportFilter) ([]DMARCSourceSummary, error) {
	args, ok := f.scopeArgs()
	if !ok {
		return nil, nil
	}
	rows, err := d.db.Query(
		`SELECT COALESCE(dm.domainname, ''), r.header_from, r.source_ip, SUM(r.msg_count),
		   SUM(CASE WHEN r.dkim_eval = 'pass' THEN r.msg_count ELSE 0 END),
		   SUM(CASE WHEN r.spf_eval = 'pass' THEN r.msg_count ELSE 0 END),
		   SUM(CASE WHEN r.dkim_eval = 'pass' OR r.spf_eval = 'pass' THEN r.msg_count ELSE 0 END),
		   SUM(CASE WHEN r.disposition = 'quarantine' THEN r.msg_count ELSE 0 END),
		   SUM(CASE WHEN r.disposition = 'reject' THEN r.msg_count ELSE 0 END)
		 FROM dmarc_report_records r
		 JOIN dmarc_reports p ON p.id = r.dmarc_report_id
		 LEFT JOIN domains dm ON dm.id = p.domain_id
		 WHERE (? OR FIND_IN_SET(p.domain_id, ?) > 0) AND p.date_begin >= ? AND (? = 0 OR p.date_begin < ?)
		 GROUP BY dm.domainname, r.header_from, r.source_ip
		 ORDER BY SUM(r.msg_count) DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DMARCSourceSummary
	for rows.Next() {
		var s DMARCSourceSummary
		if err := rows.Scan(&s.Domain, &s.HeaderFrom, &s.SourceIP, &s.Messages, &s.DKIMPass, &s.SPFPass,
			&s.DMARCPass, &s.Quarantined, &s.Rejected); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TLSPolicySummary totals the sessions reported against one policy across every
// TLS report in the filter's period.
type TLSPolicySummary struct {
	Domain       string
	PolicyDomain string
	PolicyType   string
	MXHost       string
	Success      int64
	Failure      int64
}

// TLSSummary totals TLS report sessions by domain and policy, the policies with
// the most failed sessions first.
func (d *SQLDirectory) TLSSummary(f ReportFilter) ([]TLSPolicySummary, error) {
	args, ok := f.scopeArgs()
	if !ok {
		return nil, nil
	}
	rows, err := d.db.Query(
		`SELECT COALESCE(dm.domainname, ''), t.policy_domain, t.policy_type, t.mx_host,
		   SUM(t.success_count), SUM(t.failure_count)
		 FROM tlsrpt_report_policies t
		 JOIN tlsrpt_reports p ON p.id = t.tlsrpt_report_id
		 LEFT JOIN domains dm ON dm.id = p.domain_id
		 WHERE (? OR FIND_IN_SET(p.domain_id, ?) > 0) AND p.date_begin >= ? AND (? = 0 OR p.date_begin < ?)
		 GROUP BY dm.domainname, t.policy_domain, t.policy_type, t.mx_host
		 ORDER BY SUM(t.failure_count) DESC, SUM(t.success_count) DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TLSPolicySummary
	for rows.Next() {
		var s TLSPolicySummary
		if err := rows.Scan(&s.Domain, &s.PolicyDomain, &s.PolicyType, &s.MXHost, &s.Success, &s.Failure); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
