package relay

import (
	"encoding/json"
	"fmt"
	"time"

	"hermex/internal/mailreport"
)

// maxDMARCAuthResults bounds how many raw authentication results one counter row
// keeps. A sender chooses how many DKIM signatures a message carries, and each
// distinct list is its own row, so the bound keeps a hostile message from growing
// the row key without limit.
const maxDMARCAuthResults = 8

// DMARCObservation is one inbound message's DMARC evaluation, the unit a DMARC
// aggregate report (RFC 7489 §7.2) counts. PolicyDomain is the From-header domain
// whose published record was evaluated; it is also the report row's header_from.
type DMARCObservation struct {
	PolicyDomain string
	SourceIP     string
	EnvelopeFrom string // the envelope sender's domain; "" for a null sender
	DKIM         string // the policy-evaluated DKIM verdict: pass or fail
	SPF          string // the policy-evaluated SPF verdict: pass or fail
	Disposition  string // what DMARC applied: none, quarantine or reject
	DKIMResults  []mailreport.AuthResult
	SPFResults   []mailreport.AuthResult
}

// RecordDMARC counts one message's DMARC evaluation for the daily aggregate report
// of its policy domain. Messages with the same identifiers and results on the same
// UTC day share one row, so a report row carries their count.
func (s *Spool) RecordDMARC(now time.Time, o DMARCObservation) error {
	dkim, err := encodeAuthResults(o.DKIMResults)
	if err != nil {
		return err
	}
	spf, err := encodeAuthResults(o.SPFResults)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
INSERT INTO dmarc_counters (report_day, policy_domain, source_ip, envelope_from, dkim_eval, spf_eval, disposition, dkim_results, spf_results, messages)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
ON CONFLICT(report_day, policy_domain, source_ip, envelope_from, dkim_eval, spf_eval, disposition, dkim_results, spf_results)
DO UPDATE SET messages = messages + 1`,
		now.UTC().Format(dayFormat), o.PolicyDomain, o.SourceIP, o.EnvelopeFrom, o.DKIM, o.SPF, o.Disposition, dkim, spf)
	return err
}

// encodeAuthResults renders a result list as the JSON a counter row stores, keeping
// at most maxDMARCAuthResults entries. A nil list is stored as an empty one, so the
// same results always produce the same key.
func encodeAuthResults(in []mailreport.AuthResult) (string, error) {
	if len(in) > maxDMARCAuthResults {
		in = in[:maxDMARCAuthResults]
	}
	if in == nil {
		in = []mailreport.AuthResult{}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("relay: encode DMARC auth results: %w", err)
	}
	return string(b), nil
}

// DMARCRecords returns the report rows recorded for one policy domain over the UTC
// day that contains day, in a stable order. An empty result means nothing was
// recorded and no report is due.
func (s *Spool) DMARCRecords(day time.Time, policyDomain string) ([]mailreport.Record, error) {
	d := day.UTC().Truncate(24 * time.Hour).Format(dayFormat)
	rows, err := s.db.Query(`
SELECT source_ip, envelope_from, dkim_eval, spf_eval, disposition, dkim_results, spf_results, messages
  FROM dmarc_counters
 WHERE report_day = ? AND policy_domain = ?
 ORDER BY source_ip, envelope_from, dkim_eval, spf_eval, disposition, dkim_results, spf_results`,
		d, policyDomain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mailreport.Record
	for rows.Next() {
		r := mailreport.Record{HeaderFrom: policyDomain}
		var dkim, spf string
		if err := rows.Scan(&r.SourceIP, &r.EnvelopeFrom, &r.DKIM, &r.SPF, &r.Disposition, &dkim, &spf, &r.Count); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(dkim), &r.DKIMResults); err != nil {
			return nil, fmt.Errorf("relay: decode DMARC DKIM results: %w", err)
		}
		if err := json.Unmarshal([]byte(spf), &r.SPFResults); err != nil {
			return nil, fmt.Errorf("relay: decode DMARC SPF results: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UnreportedDMARCDomains returns the policy domains that recorded DMARC counters
// for the UTC day containing day and whose report has not been dispatched yet.
func (s *Spool) UnreportedDMARCDomains(day time.Time) ([]string, error) {
	d := day.UTC().Truncate(24 * time.Hour).Format(dayFormat)
	rows, err := s.db.Query(`
SELECT DISTINCT c.policy_domain
  FROM dmarc_counters c
 WHERE c.report_day = ?
   AND NOT EXISTS (
       SELECT 1 FROM dmarc_reports_sent r
        WHERE r.report_day = c.report_day AND r.policy_domain = c.policy_domain)
 ORDER BY c.policy_domain`, d)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var domains []string
	for rows.Next() {
		var dom string
		if err := rows.Scan(&dom); err != nil {
			return nil, err
		}
		domains = append(domains, dom)
	}
	return domains, rows.Err()
}

// MarkDMARCReported records that the report for (day, policyDomain) was dispatched,
// so a later pass over the same day does not send it again. A second call for the
// same key is ignored.
func (s *Spool) MarkDMARCReported(day time.Time, policyDomain string, now time.Time) error {
	d := day.UTC().Truncate(24 * time.Hour).Format(dayFormat)
	_, err := s.db.Exec(`
INSERT INTO dmarc_reports_sent (report_day, policy_domain, sent_at)
VALUES (?, ?, ?)
ON CONFLICT(report_day, policy_domain) DO NOTHING`, d, policyDomain, now.Unix())
	return err
}

// PruneDMARCReports deletes DMARC counters and dispatch records for report days
// strictly before the UTC day containing before, bounding both tables.
func (s *Spool) PruneDMARCReports(before time.Time) error {
	cutoff := before.UTC().Truncate(24 * time.Hour).Format(dayFormat)
	if _, err := s.db.Exec(`DELETE FROM dmarc_counters WHERE report_day < ?`, cutoff); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM dmarc_reports_sent WHERE report_day < ?`, cutoff)
	return err
}
