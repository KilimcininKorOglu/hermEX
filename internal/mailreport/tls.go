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
	return parseTLSDocument(data)
}

// ParseTLS reads a TLS report that arrived outside mail: the body of an RFC 8460
// §3 HTTPS POST. gzipped selects the application/tlsrpt+gzip form, which is
// decompressed under the same bound as a mailed report.
func ParseTLS(body []byte, gzipped bool) (Result, error) {
	data := body
	if gzipped {
		var err error
		if data, err = gunzip(body); err != nil {
			return Result{}, err
		}
	} else if len(data) > maxReportBytes {
		return Result{}, ErrTooLarge
	}
	return parseTLSDocument(data)
}

// parseTLSDocument decodes a TLS report's JSON document and applies the row bound.
func parseTLSDocument(data []byte) (Result, error) {
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
