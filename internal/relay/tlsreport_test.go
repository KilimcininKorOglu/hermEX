package relay

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/ssrfguard"
	"hermex/internal/tlsrpt"
)

// reportDay is a fixed UTC day used across the report tests.
var reportDay = time.Unix(1_700_000_000, 0).UTC()

// seedCounters records a successful and a failed session for one domain, so a
// report has both a success count and a failure detail to carry.
func seedCounters(t *testing.T, sp *Spool, domain string) {
	t.Helper()
	if err := sp.RecordTLS(reportDay, domain, "sts", "mx."+domain, ""); err != nil {
		t.Fatal(err)
	}
	if err := sp.RecordTLS(reportDay, domain, "sts", "mx."+domain, "certificate-expired"); err != nil {
		t.Fatal(err)
	}
}

// fixedResolver builds a tlsrpt.Resolver whose _smtp._tls lookup returns a canned
// policy record, so discovery is deterministic and network-free.
func fixedResolver(record string) *tlsrpt.Resolver {
	return &tlsrpt.Resolver{LookupTXT: func(name string) ([]string, error) {
		if strings.HasPrefix(name, "_smtp._tls.") && record != "" {
			return []string{record}, nil
		}
		return nil, nil
	}}
}

// TestTLSReportHTTPSDelivery proves the daily pass POSTs a gzip-compressed report to
// a domain's https rua endpoint as application/tlsrpt+gzip, and that the body
// decompresses to the aggregate report for that domain.
func TestTLSReportHTTPSDelivery(t *testing.T) {
	sp := openSpool(t)
	seedCounters(t, sp, "example.test")

	var gotCT string
	var gotBody []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := &Worker{
		Spool:         sp,
		TLSResolver:   fixedResolver("v=TLSRPTv1; rua=" + srv.URL),
		TLSHTTPClient: srv.Client(),
		ReportOrg:     "hermex.test",
		ReportContact: "mailto:postmaster@hermex.test",
		ReportDomain:  "hermex.test",
	}
	if err := w.sendTLSReports(context.Background(), reportDay); err != nil {
		t.Fatalf("sendTLSReports: %v", err)
	}

	if gotCT != "application/tlsrpt+gzip" {
		t.Errorf("Content-Type = %q, want application/tlsrpt+gzip", gotCT)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gotBody))
	if err != nil {
		t.Fatalf("report body is not gzip: %v", err)
	}
	raw, _ := io.ReadAll(zr)
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("decode report JSON: %v", err)
	}
	if report["organization-name"] != "hermex.test" {
		t.Errorf("report organization-name = %v, want hermex.test", report["organization-name"])
	}
	if _, ok := report["policies"]; !ok {
		t.Error("report has no policies section")
	}
}

// TestTLSReportHTTPSRefusesInternalTarget proves the guarded client refuses to
// deliver a report to an rua endpoint that resolves to a non-public address, so a
// hostile rua= cannot turn report delivery into an SSRF probe of internal services.
func TestTLSReportHTTPSRefusesInternalTarget(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	w := &Worker{TLSHTTPClient: ssrfguard.Client(false)}
	if err := w.deliverReportHTTPS(srv.URL, []byte(`{"organization-name":"x"}`)); err == nil {
		t.Fatal("delivery to an internal https target was allowed; the SSRF guard did not block it")
	}
}

// TestTLSReportMailtoDelivery proves a mailto rua target queues one report email to
// that address through the relay spool.
func TestTLSReportMailtoDelivery(t *testing.T) {
	sp := openSpool(t)
	seedCounters(t, sp, "example.test")

	w := &Worker{
		Spool:        sp,
		TLSResolver:  fixedResolver("v=TLSRPTv1; rua=mailto:tls-reports@example.test"),
		ReportOrg:    "hermex.test",
		ReportDomain: "hermex.test",
	}
	if err := w.sendTLSReports(context.Background(), reportDay); err != nil {
		t.Fatalf("sendTLSReports: %v", err)
	}
	queued, err := sp.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Recipient != "tls-reports@example.test" {
		t.Fatalf("queued = %+v, want one report to tls-reports@example.test", queued)
	}
}

// TestTLSReportIsIdempotent proves a domain's report is dispatched at most once per
// day: a second pass over the same day sends nothing more.
func TestTLSReportIsIdempotent(t *testing.T) {
	sp := openSpool(t)
	seedCounters(t, sp, "example.test")
	w := &Worker{
		Spool:        sp,
		TLSResolver:  fixedResolver("v=TLSRPTv1; rua=mailto:tls-reports@example.test"),
		ReportDomain: "hermex.test",
	}
	if err := w.sendTLSReports(context.Background(), reportDay); err != nil {
		t.Fatal(err)
	}
	if err := w.sendTLSReports(context.Background(), reportDay); err != nil {
		t.Fatal(err)
	}
	queued, err := sp.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Errorf("queued %d report(s) after two passes, want 1 (idempotent)", len(queued))
	}
}

// TestTLSReportNoRUAMarksReported proves a domain that publishes no rua endpoint is
// marked reported (so it is not re-discovered every pass) but nothing is sent.
func TestTLSReportNoRUAMarksReported(t *testing.T) {
	sp := openSpool(t)
	seedCounters(t, sp, "example.test")
	w := &Worker{
		Spool:        sp,
		TLSResolver:  fixedResolver(""), // no TLS-RPT record published
		ReportDomain: "hermex.test",
	}
	if err := w.sendTLSReports(context.Background(), reportDay); err != nil {
		t.Fatal(err)
	}
	queued, err := sp.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Errorf("queued %d message(s) for a domain with no rua, want 0", len(queued))
	}
	left, err := sp.UnreportedDomains(reportDay)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("domain still unreported after a no-rua pass: %v (it would be re-discovered every day)", left)
	}
}

// TestPruneTLSReports proves old counters and dispatch records are deleted while a
// recent day's rows survive, bounding both tables.
func TestPruneTLSReports(t *testing.T) {
	sp := openSpool(t)
	oldDay := reportDay.AddDate(0, 0, -30)
	if err := sp.RecordTLS(oldDay, "old.test", "sts", "mx.old.test", ""); err != nil {
		t.Fatal(err)
	}
	if err := sp.RecordTLS(reportDay, "new.test", "sts", "mx.new.test", ""); err != nil {
		t.Fatal(err)
	}
	if err := sp.PruneTLSReports(reportDay.AddDate(0, 0, -7)); err != nil {
		t.Fatal(err)
	}
	if left, _ := sp.UnreportedDomains(oldDay); len(left) != 0 {
		t.Errorf("old day counters survived pruning: %v", left)
	}
	if left, _ := sp.UnreportedDomains(reportDay); len(left) != 1 || left[0] != "new.test" {
		t.Errorf("recent day counters were lost: %v", left)
	}
}

// TestBuildReportMail proves the report email carries the RFC 8460 subject, the
// application/tlsrpt+gzip attachment, and the conventional attachment filename.
func TestBuildReportMail(t *testing.T) {
	report := &tlsrpt.Report{
		OrganizationName: "hermex.test",
		DateRange:        tlsrpt.DateRange{Start: reportDay, End: reportDay.Add(24*time.Hour - time.Second)},
		ReportID:         "rid.example.test@hermex.test",
	}
	gz, err := gzipBytes([]byte(`{"organization-name":"hermex.test"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := buildReportMail("hermex.test", "tls-reports@example.test", "example.test", report, gz)
	if err != nil {
		t.Fatalf("buildReportMail: %v", err)
	}
	s := string(raw)
	for _, want := range []string{
		"Report Domain: example.test",
		"application/tlsrpt+gzip",
		"hermex.test!example.test!",
		".json.gz",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("report email missing %q:\n%s", want, s)
		}
	}
}
