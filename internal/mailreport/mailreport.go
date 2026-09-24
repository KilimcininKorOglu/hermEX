// Package mailreport reads the machine reports other mail servers send to a
// domain's reporting address: DMARC aggregate reports (RFC 7489 §7.2), SMTP TLS
// reports (RFC 8460) and DMARC failure reports in the Authentication Failure
// Reporting Format (RFC 6591).
//
// It only parses. It knows nothing about which domains this server hosts or
// where a report is stored, so the caller decides whether to trust a report.
// Every report arrives from an unauthenticated sender, so every size the parser
// reads is bounded.
package mailreport

import (
	"errors"
	"strings"

	"hermex/internal/mime"
	"hermex/internal/tlsrpt"
)

// Kind names the report family a message carried.
type Kind string

// The three report families.
const (
	KindDMARCAggregate Kind = "dmarc"
	KindTLS            Kind = "tlsrpt"
	KindDMARCFailure   Kind = "failure"
)

// Parser bounds. They protect the process from a hostile report, so they are
// fixed rather than tunable: a compressed attachment of a few kilobytes can expand
// to gigabytes, and a report with millions of rows is an attack, not a report.
const (
	// maxReportBytes caps a report document after decompression.
	maxReportBytes = 32 << 20
	// maxZipEntries caps how many archive entries are examined for the XML
	// document. Reporters send one entry.
	maxZipEntries = 16
	// maxRecords caps the rows of one DMARC aggregate report.
	maxRecords = 50000
	// maxHeaderBytes caps the original message header kept from a failure report.
	maxHeaderBytes = 64 << 10
)

var (
	// ErrNotReport means the message carries none of the three report families.
	ErrNotReport = errors.New("mailreport: the message carries no report")
	// ErrTooLarge means a report document expands past maxReportBytes.
	ErrTooLarge = errors.New("mailreport: the report exceeds the size limit")
	// ErrTooManyRecords means a DMARC aggregate report holds more than maxRecords rows.
	ErrTooManyRecords = errors.New("mailreport: the report holds too many records")
)

// Result is one parsed report. Exactly one of Aggregate, TLS and Failure is set,
// the one Kind names.
type Result struct {
	Kind      Kind
	Aggregate *Aggregate
	TLS       *tlsrpt.Report
	Failure   *Failure
}

// Domains returns every domain the report speaks for, lowercased and without a
// trailing dot. A TLS report can list several policies, and each names its own
// domain, so the caller must accept every one of them before it trusts the report.
func (r Result) Domains() []string {
	switch r.Kind {
	case KindDMARCAggregate:
		return []string{normalizeDomain(r.Aggregate.Domain)}
	case KindDMARCFailure:
		return []string{normalizeDomain(r.Failure.ReportedDomain)}
	case KindTLS:
		out := make([]string, 0, len(r.TLS.Policies))
		for _, p := range r.TLS.Policies {
			out = append(out, normalizeDomain(p.Policy.PolicyDomain))
		}
		return out
	}
	return nil
}

// ReportID returns the reporter's own identifier for the report, empty for a
// failure report, which carries none.
func (r Result) ReportID() string {
	switch r.Kind {
	case KindDMARCAggregate:
		return r.Aggregate.ReportID
	case KindTLS:
		return r.TLS.ReportID
	}
	return ""
}

// Extract finds the report a message carries and parses it. It returns
// ErrNotReport when the message holds none, so the caller can tell ordinary mail
// to the reporting address from a report it could not read.
func Extract(raw []byte) (Result, error) {
	root := mime.ParseStructure(raw)
	if isFeedbackReport(root) {
		return parseFailure(root)
	}
	return extractAttachment(root)
}

// extractAttachment walks the tree depth first and parses the first part that
// carries an aggregate or TLS report. A part that looks like a report and fails to
// parse ends the walk with that error, because a second report in one message is
// not something reporters send.
func extractAttachment(p *mime.Part) (Result, error) {
	if len(p.Children) > 0 {
		for _, c := range p.Children {
			res, err := extractAttachment(c)
			if !errors.Is(err, ErrNotReport) {
				return res, err
			}
		}
		return Result{}, ErrNotReport
	}
	switch classify(p) {
	case formatTLSGzip, formatTLSJSON:
		return parseTLSPart(p)
	case formatZip, formatGzip, formatXML:
		return parseAggregatePart(p)
	}
	return Result{}, ErrNotReport
}

// normalizeDomain lowercases a domain and drops a trailing dot and surrounding
// space, so a comparison with a recipient's domain is exact.
func normalizeDomain(d string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
}
