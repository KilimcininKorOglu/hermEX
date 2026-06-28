package relay

import (
	"time"

	"hermex/internal/tlsrpt"
)

// dayFormat is the report-day key: a UTC calendar date (RFC 8460 reports cover a
// full UTC day).
const dayFormat = "2006-01-02"

// RecordTLS counts one outbound TLS session outcome for the daily TLS-RPT report
// (RFC 8460). resultType is "" for a successful session, otherwise a result-type
// constant. The row is keyed by the UTC report day of now, the recipient domain,
// the policy type that governed the attempt, and the mail exchanger, so repeated
// outcomes accumulate. It satisfies the worker's TLSReporter, letting the spool
// double as the report store.
func (s *Spool) RecordTLS(now time.Time, policyDomain, policyType, mxHost, resultType string) error {
	_, err := s.db.Exec(`
INSERT INTO tlsrpt_counters (report_day, policy_domain, policy_type, mx_host, result_type, sessions)
VALUES (?, ?, ?, ?, ?, 1)
ON CONFLICT(report_day, policy_domain, policy_type, mx_host, result_type)
DO UPDATE SET sessions = sessions + 1`,
		now.UTC().Format(dayFormat), policyDomain, policyType, mxHost, resultType)
	return err
}

// TLSReport assembles the daily aggregate TLS report (RFC 8460 §4.4) for one
// recipient domain over the UTC day that contains day. It groups the recorded
// counters by policy type and mail exchanger into one report policy each, summing
// successful and failed sessions and listing each failure result type. It returns
// (nil, nil) when no sessions were recorded, so the caller can skip an empty
// report. org, contact, and reportID populate the report's identifying fields.
func (s *Spool) TLSReport(day time.Time, policyDomain, org, contact, reportID string) (*tlsrpt.Report, error) {
	d := day.UTC().Truncate(24 * time.Hour)
	rows, err := s.db.Query(`
SELECT policy_type, mx_host, result_type, sessions
  FROM tlsrpt_counters
 WHERE report_day = ? AND policy_domain = ?
 ORDER BY policy_type, mx_host, result_type`,
		d.Format(dayFormat), policyDomain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Group rows into one report policy per (policy_type, mx_host) pair, in stable
	// first-seen order so the report is deterministic.
	type key struct{ policyType, mxHost string }
	index := map[key]int{}
	var policies []tlsrpt.PolicyResult
	for rows.Next() {
		var policyType, mxHost, resultType string
		var sessions int
		if err := rows.Scan(&policyType, &mxHost, &resultType, &sessions); err != nil {
			return nil, err
		}
		k := key{policyType, mxHost}
		i, ok := index[k]
		if !ok {
			i = len(policies)
			index[k] = i
			policies = append(policies, tlsrpt.PolicyResult{
				Policy: tlsrpt.PolicyDescriptor{
					PolicyType:   policyType,
					PolicyDomain: policyDomain,
					MXHost:       mxHost,
				},
			})
		}
		if resultType == "" {
			policies[i].Summary.TotalSuccessful += sessions
			continue
		}
		policies[i].Summary.TotalFailure += sessions
		policies[i].FailureDetails = append(policies[i].FailureDetails, tlsrpt.FailureDetail{
			ResultType:          resultType,
			ReceivingMXHostname: mxHost,
			FailedSessionCount:  sessions,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(policies) == 0 {
		return nil, nil
	}
	return &tlsrpt.Report{
		OrganizationName: org,
		DateRange: tlsrpt.DateRange{
			Start: d,
			End:   d.Add(24*time.Hour - time.Second),
		},
		ContactInfo: contact,
		ReportID:    reportID,
		Policies:    policies,
	}, nil
}
