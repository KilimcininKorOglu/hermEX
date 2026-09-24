package mailreport

import (
	"encoding/json"
	"fmt"

	"hermex/internal/mime"
	"hermex/internal/tlsrpt"
)

// parseTLSPart reads an SMTP TLS report (RFC 8460 §4.4) from one attachment. A
// document with no report id or no policy is not a TLS report, whatever its
// label says.
func parseTLSPart(p *mime.Part) (Result, error) {
	data, err := tlsDocument(p)
	if err != nil {
		return Result{}, err
	}
	var rep tlsrpt.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return Result{}, fmt.Errorf("mailreport: TLS report JSON: %w", err)
	}
	if rep.ReportID == "" || len(rep.Policies) == 0 {
		return Result{}, ErrNotReport
	}
	if tlsRows(&rep) > maxRecords {
		return Result{}, ErrTooManyRecords
	}
	return Result{Kind: KindTLS, TLS: &rep}, nil
}

// tlsDocument returns the attachment's JSON document, decompressed.
func tlsDocument(p *mime.Part) ([]byte, error) {
	data, err := p.DecodedContent()
	if err != nil {
		return nil, err
	}
	if classify(p) == formatTLSGzip {
		return gunzip(data)
	}
	if len(data) > maxReportBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

// tlsRows counts what storing the report writes: one row per policy and one per
// failure detail.
func tlsRows(rep *tlsrpt.Report) int {
	n := len(rep.Policies)
	for _, p := range rep.Policies {
		n += len(p.FailureDetails)
	}
	return n
}
