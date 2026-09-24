package directory

import (
	"errors"
	"testing"
	"time"

	"hermex/internal/mailreport"
	"hermex/internal/tlsrpt"
)

// Report periods used below: day one and day two of the tests' calendar.
var (
	reportDay1 = time.Date(2025, 9, 19, 0, 0, 0, 0, time.UTC)
	reportDay2 = reportDay1.Add(24 * time.Hour)
)

func sampleAggregate(reportID string, begin time.Time) *mailreport.Aggregate {
	return &mailreport.Aggregate{
		OrgName: "google.com", Email: "noreply@google.com", ReportID: reportID,
		Begin: begin, End: begin.Add(24*time.Hour - time.Second),
		Domain: "acme.test", ADKIM: "r", ASPF: "r", P: "quarantine", SP: "none", Pct: "100",
		Records: []mailreport.Record{
			{SourceIP: "192.0.2.1", Count: 10, Disposition: "none", DKIM: "pass", SPF: "pass",
				HeaderFrom: "acme.test", EnvelopeFrom: "acme.test",
				DKIMResults: []mailreport.AuthResult{{Domain: "acme.test", Selector: "s1", Result: "pass"}},
				SPFResults:  []mailreport.AuthResult{{Domain: "acme.test", Result: "pass"}}},
			{SourceIP: "203.0.113.9", Count: 4, Disposition: "quarantine", DKIM: "fail", SPF: "fail",
				HeaderFrom: "acme.test"},
		},
	}
}

func sampleTLS(reportID string, begin time.Time) *tlsrpt.Report {
	return &tlsrpt.Report{
		OrganizationName: "Google Inc.", ContactInfo: "tls@google.com", ReportID: reportID,
		DateRange: tlsrpt.DateRange{Start: begin, End: begin.Add(24*time.Hour - time.Second)},
		Policies: []tlsrpt.PolicyResult{{
			Policy:  tlsrpt.PolicyDescriptor{PolicyType: "sts", PolicyDomain: "acme.test", MXHost: "mail.acme.test"},
			Summary: tlsrpt.Summary{TotalSuccessful: 40, TotalFailure: 2},
			FailureDetails: []tlsrpt.FailureDetail{{ResultType: "certificate-expired", SendingMTAIP: "192.0.2.9",
				FailedSessionCount: 2}},
		}},
	}
}

func source(domainID int64, received time.Time) ReportSource {
	return ReportSource{DomainID: domainID, ReceivedAt: received.Unix(), MailFrom: "noreply@google.com", RemoteAddr: "192.0.2.50:25"}
}

func TestDMARCAggregateRoundTrip(t *testing.T) {
	d, _ := freshDirectory(t)
	dom := mustCreateDomain(t, d, t.TempDir(), "acme.test")

	id, err := d.StoreDMARCAggregate(source(dom, reportDay2), sampleAggregate("r-1", reportDay1))
	mustNoErr(t, "store the report", err)

	r, ok, err := d.GetDMARCReport(id)
	mustNoErr(t, "read the report", err)
	wantEq(t, "report found", ok, true)
	wantEq(t, "the domain", r.Domain, "acme.test")
	wantEq(t, "the policy", r.PolicyP, "quarantine")
	wantEq(t, "the record count", len(r.Records), 2)
	wantEq(t, "the busiest source first", r.Records[0].SourceIP, "192.0.2.1")
	wantEq(t, "the DKIM results line", r.Records[0].DKIMResults, "acme.test/s1=pass")
	wantEq(t, "the message total", r.Total, int64(14))
	wantEq(t, "the failed messages", r.Failed, int64(4))
}

// TestAResentReportIsStoredOnce holds the duplicate rule: a reporter that is not
// sure a report arrived sends it again, and storing both would double every count
// in the summary.
func TestAResentReportIsStoredOnce(t *testing.T) {
	d, _ := freshDirectory(t)
	dom := mustCreateDomain(t, d, t.TempDir(), "acme.test")

	_, err := d.StoreDMARCAggregate(source(dom, reportDay2), sampleAggregate("r-1", reportDay1))
	mustNoErr(t, "store the report", err)
	_, err = d.StoreDMARCAggregate(source(dom, reportDay2), sampleAggregate("r-1", reportDay1))
	if !errors.Is(err, ErrDuplicateReport) {
		t.Fatalf("second aggregate store err = %v, want ErrDuplicateReport", err)
	}
	_, err = d.StoreTLSReport(source(dom, reportDay2), sampleTLS("t-1", reportDay1))
	mustNoErr(t, "store the TLS report", err)
	_, err = d.StoreTLSReport(source(dom, reportDay2), sampleTLS("t-1", reportDay1))
	if !errors.Is(err, ErrDuplicateReport) {
		t.Fatalf("second TLS store err = %v, want ErrDuplicateReport", err)
	}

	sum, err := d.DMARCSummary(ReportFilter{All: true})
	mustNoErr(t, "summarize", err)
	wantEq(t, "the summarized messages of the main source", sum[0].Messages, int64(10))
}

func TestTLSReportRoundTrip(t *testing.T) {
	d, _ := freshDirectory(t)
	dom := mustCreateDomain(t, d, t.TempDir(), "acme.test")

	id, err := d.StoreTLSReport(source(dom, reportDay2), sampleTLS("t-1", reportDay1))
	mustNoErr(t, "store the report", err)
	r, ok, err := d.GetTLSReport(id)
	mustNoErr(t, "read the report", err)
	wantEq(t, "report found", ok, true)
	wantEq(t, "the policy count", len(r.Policies), 1)
	wantEq(t, "the failed sessions", r.Failed, int64(2))
	wantEq(t, "the failure detail", r.Policies[0].FailureDetails[0].ResultType, "certificate-expired")
}

func TestDMARCFailureRoundTrip(t *testing.T) {
	d, _ := freshDirectory(t)
	dom := mustCreateDomain(t, d, t.TempDir(), "acme.test")

	f := &mailreport.Failure{ArrivalDate: reportDay1, SourceIP: "198.51.100.3", ReportedDomain: "acme.test",
		AuthFailure: "dmarc", OriginalMailFrom: "<x@evil.example>", OriginalHeaders: "Subject: hi\r\n\r\n"}
	id, err := d.StoreDMARCFailure(source(dom, reportDay2), f)
	mustNoErr(t, "store the failure report", err)
	got, ok, err := d.GetDMARCFailure(id)
	mustNoErr(t, "read the failure report", err)
	wantEq(t, "report found", ok, true)
	wantEq(t, "the source", got.SourceIP, "198.51.100.3")
	wantEq(t, "the arrival", got.ArrivalDate, reportDay1.Unix())
	wantEq(t, "the headers", got.OriginalHeaders, "Subject: hi\r\n\r\n")
}

// TestReportListsFollowTheAdminScope is the tenant boundary of the report pages: a
// domain admin sees only their own domains' reports.
func TestReportListsFollowTheAdminScope(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	acme := mustCreateDomain(t, d, root, "acme.test")
	other := mustCreateDomain(t, d, root, "other.test")

	_, err := d.StoreDMARCAggregate(source(acme, reportDay2), sampleAggregate("r-1", reportDay1))
	mustNoErr(t, "store acme's report", err)
	_, err = d.StoreTLSReport(source(acme, reportDay2), sampleTLS("t-1", reportDay1))
	mustNoErr(t, "store acme's TLS report", err)
	_, err = d.StoreDMARCFailure(source(acme, reportDay2), &mailreport.Failure{ReportedDomain: "acme.test"})
	mustNoErr(t, "store acme's failure report", err)

	for _, c := range []struct {
		name string
		f    ReportFilter
		want int
	}{
		{"a system admin", ReportFilter{All: true}, 1},
		{"acme's admin", ReportFilter{DomainIDs: []int64{acme}}, 1},
		{"another domain's admin", ReportFilter{DomainIDs: []int64{other}}, 0},
		{"an empty scope", ReportFilter{}, 0},
	} {
		dm, err := d.ListDMARCReports(c.f)
		mustNoErr(t, "list aggregate reports", err)
		tl, err := d.ListTLSReports(c.f)
		mustNoErr(t, "list TLS reports", err)
		fl, err := d.ListDMARCFailures(c.f)
		mustNoErr(t, "list failure reports", err)
		sm, err := d.DMARCSummary(c.f)
		mustNoErr(t, "summarize", err)
		wantEq(t, c.name+": aggregate reports", len(dm), c.want)
		wantEq(t, c.name+": TLS reports", len(tl), c.want)
		wantEq(t, c.name+": failure reports", len(fl), c.want)
		wantEq(t, c.name+": summary sources", len(sm), 2*c.want)
	}
}

func TestReportPeriodFilter(t *testing.T) {
	d, _ := freshDirectory(t)
	dom := mustCreateDomain(t, d, t.TempDir(), "acme.test")
	_, err := d.StoreDMARCAggregate(source(dom, reportDay2), sampleAggregate("day-1", reportDay1))
	mustNoErr(t, "store day one", err)
	_, err = d.StoreDMARCAggregate(source(dom, reportDay2), sampleAggregate("day-2", reportDay2))
	mustNoErr(t, "store day two", err)

	got, err := d.ListDMARCReports(ReportFilter{All: true, From: reportDay2.Unix()})
	mustNoErr(t, "list from day two", err)
	wantEq(t, "reports from day two", len(got), 1)
	wantEq(t, "the day two report", got[0].ReportID, "day-2")
	got, err = d.ListDMARCReports(ReportFilter{All: true, To: reportDay2.Unix()})
	mustNoErr(t, "list before day two", err)
	wantEq(t, "reports before day two", len(got), 1)
	wantEq(t, "the day one report", got[0].ReportID, "day-1")
}

func TestSummaryTotals(t *testing.T) {
	d, _ := freshDirectory(t)
	dom := mustCreateDomain(t, d, t.TempDir(), "acme.test")
	_, err := d.StoreDMARCAggregate(source(dom, reportDay2), sampleAggregate("r-1", reportDay1))
	mustNoErr(t, "store day one", err)
	_, err = d.StoreDMARCAggregate(source(dom, reportDay2), sampleAggregate("r-2", reportDay2))
	mustNoErr(t, "store day two", err)

	sum, err := d.DMARCSummary(ReportFilter{All: true})
	mustNoErr(t, "summarize", err)
	wantEq(t, "the source rows", len(sum), 2)
	wantEq(t, "the busiest source", sum[0].SourceIP, "192.0.2.1")
	wantEq(t, "its messages over both days", sum[0].Messages, int64(20))
	wantEq(t, "its DMARC passes", sum[0].DMARCPass, int64(20))
	wantEq(t, "the failing source's quarantined messages", sum[1].Quarantined, int64(8))
	wantEq(t, "the failing source's DMARC passes", sum[1].DMARCPass, int64(0))

	_, err = d.StoreTLSReport(source(dom, reportDay2), sampleTLS("t-1", reportDay1))
	mustNoErr(t, "store a TLS report", err)
	tls, err := d.TLSSummary(ReportFilter{All: true})
	mustNoErr(t, "summarize TLS", err)
	wantEq(t, "the TLS policy rows", len(tls), 1)
	wantEq(t, "the TLS failures", tls[0].Failure, int64(2))
}

// TestPruneMailReportsHonoursEachWindow proves the sweep keeps each family for its
// own window: failure reports carry personal data and go sooner than the counts.
func TestPruneMailReportsHonoursEachWindow(t *testing.T) {
	d, db := freshDirectory(t)
	dom := mustCreateDomain(t, d, t.TempDir(), "acme.test")
	now := reportDay1.Add(100 * 24 * time.Hour)
	old := now.Add(-40 * 24 * time.Hour)

	aggID, err := d.StoreDMARCAggregate(source(dom, old), sampleAggregate("r-1", reportDay1))
	mustNoErr(t, "store an aggregate report", err)
	_, err = d.StoreDMARCFailure(source(dom, old), &mailreport.Failure{ReportedDomain: "acme.test"})
	mustNoErr(t, "store a failure report", err)

	// The defaults keep a 40-day-old aggregate report and delete the failure report.
	n, err := d.PruneMailReports(now)
	mustNoErr(t, "prune with the defaults", err)
	wantEq(t, "reports deleted under the defaults", n, int64(1))
	_, ok, _ := d.GetDMARCReport(aggID)
	wantEq(t, "the aggregate report kept", ok, true)

	mustNoErr(t, "shorten the window", d.SetMailReportSettings(MailReportSettings{AggregateDays: 30, FailureDays: 30}))
	n, err = d.PruneMailReports(now)
	mustNoErr(t, "prune with the shorter window", err)
	wantEq(t, "reports deleted under the shorter window", n, int64(1))
	var rows int
	mustNoErr(t, "count the records left", db.QueryRow(`SELECT COUNT(*) FROM dmarc_report_records`).Scan(&rows))
	wantEq(t, "the deleted report's records", rows, 0)

	st, found, err := d.GetMailReportSettings()
	mustNoErr(t, "read the settings", err)
	wantEq(t, "settings saved", found, true)
	wantEq(t, "the aggregate window", st.AggregateDays, 30)
}
