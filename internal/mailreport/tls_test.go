package mailreport

import (
	"errors"
	"testing"
)

// TestParseTLSReadsBothPostedForms: an RFC 8460 §3 POST carries the report as
// plain JSON or gzipped, and both parse to the same report.
func TestParseTLSReadsBothPostedForms(t *testing.T) {
	doc := []byte(tlsReportJSON("hermex.test"))
	for name, res := range map[string]func() (Result, error){
		"json": func() (Result, error) { return ParseTLS(doc, false) },
		"gzip": func() (Result, error) { return ParseTLS(gzipOf(t, doc), true) },
	} {
		r, err := res()
		mustNoErr(t, name, err)
		wantEq(t, name+": kind", r.Kind, KindTLS)
		wantEq(t, name+": report id", r.ReportID(), "2025-09-19T00:00:00Z_hermex.test")
		wantEq(t, name+": policy domain", r.Domains()[0], "hermex.test")
	}
}

// TestParseTLSKeepsTheParserBounds: a posted body gets the same protection as a
// mailed one, so a compression bomb and a document that is no report are refused.
func TestParseTLSKeepsTheParserBounds(t *testing.T) {
	if _, err := ParseTLS(gzipOf(t, make([]byte, maxReportBytes+1)), true); !errors.Is(err, ErrTooLarge) {
		t.Errorf("gzip bomb: err = %v, want ErrTooLarge", err)
	}
	if _, err := ParseTLS(make([]byte, maxReportBytes+1), false); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized JSON: err = %v, want ErrTooLarge", err)
	}
	if _, err := ParseTLS([]byte(`{"report-id":"x","policies":[]}`), false); !errors.Is(err, ErrNotReport) {
		t.Errorf("no policies: err = %v, want ErrNotReport", err)
	}
	if _, err := ParseTLS([]byte("not gzip"), true); err == nil {
		t.Error("a body labelled gzip that is not gzip parsed")
	}
}
