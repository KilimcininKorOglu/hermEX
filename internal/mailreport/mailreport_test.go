package mailreport

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// aggregateXML builds a DMARC aggregate report for domain with the given record
// elements. xmlns is empty for the original schema.
func aggregateXML(xmlns, domain string, records ...string) string {
	open := "<feedback>"
	if xmlns != "" {
		open = `<feedback xmlns="` + xmlns + `">`
	}
	return `<?xml version="1.0" encoding="UTF-8"?>` + open +
		`<report_metadata><org_name>google.com</org_name><email>noreply-dmarc-support@google.com</email>` +
		`<report_id>1234567890</report_id><date_range><begin>1758240000</begin><end>1758326399</end></date_range></report_metadata>` +
		`<policy_published><domain>` + domain + `</domain><adkim>r</adkim><aspf>r</aspf><p>quarantine</p><sp>none</sp><pct>100</pct></policy_published>` +
		strings.Join(records, "") + `</feedback>`
}

// aggregateRecord builds one <record> element.
func aggregateRecord(ip string, count int, dkim, spf string) string {
	return fmt.Sprintf(`<record><row><source_ip>%s</source_ip><count>%d</count>`+
		`<policy_evaluated><disposition>none</disposition><dkim>%s</dkim><spf>%s</spf></policy_evaluated></row>`+
		`<identifiers><header_from>hermex.test</header_from><envelope_from>hermex.test</envelope_from></identifiers>`+
		`<auth_results><dkim><domain>hermex.test</domain><selector>hermex</selector><result>%s</result></dkim>`+
		`<spf><domain>hermex.test</domain><result>%s</result></spf></auth_results></record>`, ip, count, dkim, spf, dkim, spf)
}

func zipOf(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	mustNoErr(t, "create the archive entry", err)
	_, err = w.Write(content)
	mustNoErr(t, "write the archive entry", err)
	mustNoErr(t, "close the archive", zw.Close())
	return buf.Bytes()
}

func gzipOf(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(content)
	mustNoErr(t, "write the gzip stream", err)
	mustNoErr(t, "close the gzip stream", zw.Close())
	return buf.Bytes()
}

// withAttachment builds a multipart message carrying one base64 attachment, the
// shape every reporter sends.
func withAttachment(ctype, filename string, data []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(data)
	var lines []string
	for len(enc) > 76 {
		lines = append(lines, enc[:76])
		enc = enc[76:]
	}
	lines = append(lines, enc)
	return []byte("From: noreply@reporter.example\r\nTo: postmaster@hermex.test\r\nSubject: Report\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain\r\n\r\nThis is a report.\r\n" +
		"--b1\r\nContent-Type: " + ctype + "; name=\"" + filename + "\"\r\n" +
		"Content-Disposition: attachment; filename=\"" + filename + "\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + strings.Join(lines, "\r\n") + "\r\n--b1--\r\n")
}

// failureReport builds an RFC 6591 report. The attached original message carries
// a body line the parser must not keep.
func failureReport(feedbackType, domain string) []byte {
	return []byte("From: dmarc@reporter.example\r\nTo: postmaster@hermex.test\r\nSubject: Failure\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/report; report-type=feedback-report; boundary=\"r1\"\r\n\r\n" +
		"--r1\r\nContent-Type: text/plain\r\n\r\nA message failed authentication.\r\n" +
		"--r1\r\nContent-Type: message/feedback-report\r\n\r\n" +
		"Feedback-Type: " + feedbackType + "\r\nUser-Agent: reporter/1.0\r\nVersion: 1\r\n" +
		"Original-Mail-From: <spoofer@evil.example>\r\nOriginal-Rcpt-To: <bob@reporter.example>\r\n" +
		"Arrival-Date: Fri, 19 Sep 2025 10:00:00 +0000\r\nSource-IP: 192.0.2.7\r\n" +
		"Reported-Domain: " + domain + "\r\nAuth-Failure: dmarc\r\n" +
		"Authentication-Results: reporter.example; dmarc=fail header.from=" + domain + "\r\n" +
		"--r1\r\nContent-Type: message/rfc822\r\n\r\n" +
		"From: ceo@" + domain + "\r\nSubject: Wire the money\r\n\r\nSECRET BODY LINE\r\n" +
		"--r1--\r\n")
}

func TestExtractZippedAggregateReport(t *testing.T) {
	doc := aggregateXML("", "hermex.test",
		aggregateRecord("192.0.2.1", 12, "pass", "pass"),
		aggregateRecord("2001:db8::1", 3, "fail", "fail"))
	raw := withAttachment("application/zip", "google.com!hermex.test!1758240000!1758326399.zip",
		zipOf(t, "google.com!hermex.test!1758240000!1758326399.xml", []byte(doc)))

	res, err := Extract(raw)
	mustNoErr(t, "extract", err)
	wantEq(t, "the kind", res.Kind, KindDMARCAggregate)
	a := res.Aggregate
	wantEq(t, "the reporter", a.OrgName, "google.com")
	wantEq(t, "the report id", a.ReportID, "1234567890")
	wantEq(t, "the begin", a.Begin.Unix(), int64(1758240000))
	wantEq(t, "the policy", a.P, "quarantine")
	wantEq(t, "the record count", len(a.Records), 2)
	wantEq(t, "the first source", a.Records[0].SourceIP, "192.0.2.1")
	wantEq(t, "the first count", a.Records[0].Count, int64(12))
	wantEq(t, "the second DKIM verdict", a.Records[1].DKIM, "fail")
	wantEq(t, "the DKIM selector", a.Records[0].DKIMResults[0].Selector, "hermex")
	wantEq(t, "the domains", strings.Join(res.Domains(), ","), "hermex.test")
}

// TestExtractGzippedAggregateReportByName covers the reporters that label the
// attachment application/octet-stream and rely on the file name.
func TestExtractGzippedAggregateReportByName(t *testing.T) {
	doc := aggregateXML("", "Hermex.Test.", aggregateRecord("192.0.2.1", 1, "pass", "pass"))
	raw := withAttachment("application/octet-stream", "report.xml.gz", gzipOf(t, []byte(doc)))

	res, err := Extract(raw)
	mustNoErr(t, "extract", err)
	wantEq(t, "the kind", res.Kind, KindDMARCAggregate)
	wantEq(t, "the normalized domain", res.Domains()[0], "hermex.test")
}

// TestExtractNamespacedAggregateReport covers the namespaced schema of the updated
// DMARC specification.
func TestExtractNamespacedAggregateReport(t *testing.T) {
	doc := aggregateXML("urn:ietf:params:xml:ns:dmarc-2.0", "hermex.test", aggregateRecord("192.0.2.1", 5, "pass", "fail"))
	res, err := Extract(withAttachment("text/xml", "report.xml", []byte(doc)))
	mustNoErr(t, "extract", err)
	wantEq(t, "the record count", len(res.Aggregate.Records), 1)
	wantEq(t, "the SPF verdict", res.Aggregate.Records[0].SPF, "fail")
}

func tlsReportJSON(domains ...string) string {
	var policies []string
	for _, d := range domains {
		policies = append(policies, `{"policy":{"policy-type":"sts","policy-string":["version: STSv1","mode: enforce"],`+
			`"policy-domain":"`+d+`","mx-host":"mail.`+d+`"},"summary":{"total-successful-session-count":40,"total-failure-session-count":2},`+
			`"failure-details":[{"result-type":"certificate-expired","sending-mta-ip":"192.0.2.9","receiving-ip":"198.51.100.4","failed-session-count":2}]}`)
	}
	return `{"organization-name":"Google Inc.","date-range":{"start-datetime":"2025-09-19T00:00:00Z","end-datetime":"2025-09-19T23:59:59Z"},` +
		`"contact-info":"smtp-tls-reporting@google.com","report-id":"2025-09-19T00:00:00Z_hermex.test","policies":[` + strings.Join(policies, ",") + `]}`
}

func TestExtractTLSReport(t *testing.T) {
	raw := withAttachment("application/tlsrpt+gzip", "google.com!hermex.test!1758240000!1758326399!001.json.gz",
		gzipOf(t, []byte(tlsReportJSON("hermex.test"))))

	res, err := Extract(raw)
	mustNoErr(t, "extract", err)
	wantEq(t, "the kind", res.Kind, KindTLS)
	wantEq(t, "the report id", res.ReportID(), "2025-09-19T00:00:00Z_hermex.test")
	p := res.TLS.Policies[0]
	wantEq(t, "the failure count", p.Summary.TotalFailure, 2)
	wantEq(t, "the failure type", p.FailureDetails[0].ResultType, "certificate-expired")
}

// TestAGzippedTLSReportIsNotReadAsAnAggregateReport pins the name rule: some
// reporters send the TLS report with the generic gzip type, which on its own would
// route it to the aggregate parser.
func TestAGzippedTLSReportIsNotReadAsAnAggregateReport(t *testing.T) {
	raw := withAttachment("application/gzip", "report.json.gz", gzipOf(t, []byte(tlsReportJSON("hermex.test"))))
	res, err := Extract(raw)
	mustNoErr(t, "extract", err)
	wantEq(t, "the kind", res.Kind, KindTLS)
}

// TestDomainsListsEveryTLSPolicy holds the caller to checking every policy: a TLS
// report can name several domains, and trusting only the first would let a report
// file data under a domain it was never addressed to.
func TestDomainsListsEveryTLSPolicy(t *testing.T) {
	raw := withAttachment("application/tlsrpt+json", "r.json", []byte(tlsReportJSON("hermex.test", "victim.test")))
	res, err := Extract(raw)
	mustNoErr(t, "extract", err)
	wantEq(t, "the domains", strings.Join(res.Domains(), ","), "hermex.test,victim.test")
}

// TestExtractFailureReportKeepsHeadersNotBody is the privacy property of the
// failure parser: the report exists to diagnose authentication, so the failed
// message's header block is kept and its body is not.
func TestExtractFailureReportKeepsHeadersNotBody(t *testing.T) {
	res, err := Extract(failureReport("auth-failure", "hermex.test"))
	mustNoErr(t, "extract", err)
	wantEq(t, "the kind", res.Kind, KindDMARCFailure)
	f := res.Failure
	wantEq(t, "the reported domain", f.ReportedDomain, "hermex.test")
	wantEq(t, "the source", f.SourceIP, "192.0.2.7")
	wantEq(t, "the failure", f.AuthFailure, "dmarc")
	wantEq(t, "the arrival", f.ArrivalDate.Unix(), int64(1758276000))
	if !strings.Contains(f.OriginalHeaders, "Subject: Wire the money") {
		t.Errorf("the original headers were not kept: %q", f.OriginalHeaders)
	}
	if strings.Contains(f.OriginalHeaders, "SECRET BODY LINE") {
		t.Errorf("the failed message's body was kept: %q", f.OriginalHeaders)
	}
}

// TestParsingAFailureReportLeavesTheMessageIntact guards readFields: the
// feedback-report body is a slice of the whole message, and appending the header
// terminator to it in place overwrote the attached original message.
func TestParsingAFailureReportLeavesTheMessageIntact(t *testing.T) {
	raw := failureReport("auth-failure", "hermex.test")
	before := append([]byte(nil), raw...)
	_, err := Extract(raw)
	mustNoErr(t, "extract", err)
	if !bytes.Equal(raw, before) {
		t.Fatal("parsing the report modified the message bytes")
	}
}

func TestOrdinaryMailIsNotAReport(t *testing.T) {
	cases := map[string][]byte{
		"plain text": []byte("From: a@b.example\r\nSubject: hi\r\n\r\nhello\r\n"),
		"archive without XML": withAttachment("application/zip", "photos.zip",
			zipOf(t, "cat.jpg", []byte("not xml"))),
		"XML that is not a report": withAttachment("text/xml", "invoice.xml",
			[]byte(`<?xml version="1.0"?><invoice><total>1</total></invoice>`)),
		"abuse complaint":   failureReport("abuse", "hermex.test"),
		"JSON with no body": withAttachment("application/tlsrpt+json", "r.json", []byte(`{"organization-name":"x"}`)),
	}
	for name, raw := range cases {
		if _, err := Extract(raw); !errors.Is(err, ErrNotReport) {
			t.Errorf("%s: err = %v, want ErrNotReport", name, err)
		}
	}
}

// TestAnExpandingArchiveIsRefused is the decompression bound. A few kilobytes of
// gzip expand to the limit plus one byte; reading it whole would let one message
// take as much memory as the sender likes.
func TestAnExpandingArchiveIsRefused(t *testing.T) {
	bomb := gzipOf(t, make([]byte, maxReportBytes+1))
	if len(bomb) > 1<<20 {
		t.Fatalf("the test archive is %d bytes, it should compress to a few kilobytes", len(bomb))
	}
	_, err := Extract(withAttachment("application/gzip", "report.xml.gz", bomb))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	_, err = Extract(withAttachment("application/zip", "report.zip", zipOf(t, "report.xml", make([]byte, maxReportBytes+1))))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("zip: err = %v, want ErrTooLarge", err)
	}
}

func TestAReportWithTooManyRecordsIsRefused(t *testing.T) {
	rec := aggregateRecord("192.0.2.1", 1, "pass", "pass")
	records := make([]string, maxRecords+1)
	for i := range records {
		records[i] = rec
	}
	doc := aggregateXML("", "hermex.test", records...)
	_, err := Extract(withAttachment("application/gzip", "report.xml.gz", gzipOf(t, []byte(doc))))
	if !errors.Is(err, ErrTooManyRecords) {
		t.Fatalf("err = %v, want ErrTooManyRecords", err)
	}
}

func TestAMalformedRecordIsRefused(t *testing.T) {
	cases := map[string]string{
		"source is not an address": aggregateRecord("not-an-ip", 1, "pass", "pass"),
		"negative count":           aggregateRecord("192.0.2.1", -4, "pass", "pass"),
	}
	for name, rec := range cases {
		doc := aggregateXML("", "hermex.test", rec)
		_, err := Extract(withAttachment("text/xml", "r.xml", []byte(doc)))
		if err == nil || errors.Is(err, ErrNotReport) {
			t.Errorf("%s: err = %v, want a parse error", name, err)
		}
	}
}
