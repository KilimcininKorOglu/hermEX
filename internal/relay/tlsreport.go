package relay

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"hermex/internal/logging"
	"hermex/internal/tlsrpt"
)

// sendTLSReports dispatches the aggregate TLS-RPT report (RFC 8460) for every
// recipient domain that recorded outbound-TLS counters on the given UTC day and has
// not yet been reported. It is best-effort per domain: one domain's failure never
// stops the others, and each domain is marked reported exactly once so the once-daily
// pass never re-loops. The caller holds the drain guard, so only one instance runs it.
func (w *Worker) sendTLSReports(ctx context.Context, day time.Time) error {
	domains, err := w.Spool.UnreportedDomains(day)
	if err != nil {
		return err
	}
	for _, domain := range domains {
		if ctx.Err() != nil {
			return nil
		}
		w.reportDomain(day, domain)
	}
	return nil
}

// reportDomain assembles, addresses, and delivers one domain's daily report. It
// marks the domain reported for this day on every outcome (via defer): the daily
// pass runs once per day, so a transient failure loses that day's report (best-effort
// per RFC 8460) rather than re-looping the drainer. Every failure is logged.
func (w *Worker) reportDomain(day time.Time, domain string) {
	defer func() {
		if err := w.Spool.MarkReported(day, domain, time.Now()); err != nil {
			w.logReport("tlsrpt.mark.fail", domain, err)
		}
	}()

	reportID := fmt.Sprintf("%d.%s@%s", day.UTC().Truncate(24*time.Hour).Unix(), domain, w.ReportDomain)
	report, err := w.Spool.TLSReport(day, domain, w.ReportOrg, w.ReportContact, reportID)
	if err != nil {
		w.logReport("tlsrpt.assemble.fail", domain, err)
		return
	}
	if report == nil {
		return // no sessions recorded; nothing to send
	}
	policy, err := w.TLSResolver.Lookup(domain)
	if err != nil {
		w.logReport("tlsrpt.discover.fail", domain, err)
		return
	}
	if policy == nil {
		return // the domain publishes no rua endpoint; nothing to send
	}
	jsonBytes, err := report.JSON()
	if err != nil {
		w.logReport("tlsrpt.encode.fail", domain, err)
		return
	}
	for _, uri := range policy.RUAs {
		if err := w.deliverReport(uri, domain, report, jsonBytes); err != nil {
			w.logReport("tlsrpt.deliver.fail", domain, err)
		}
	}
}

// deliverReport sends one report to a single rua target, dispatching on its scheme.
func (w *Worker) deliverReport(uri, policyDomain string, report *tlsrpt.Report, jsonBytes []byte) error {
	switch {
	case strings.HasPrefix(uri, "https:"):
		return w.deliverReportHTTPS(uri, jsonBytes)
	case strings.HasPrefix(uri, "mailto:"):
		return w.deliverReportMail(uri, policyDomain, report, jsonBytes)
	default:
		return fmt.Errorf("tlsrpt: unsupported rua scheme %q", uri)
	}
}

// deliverReportHTTPS POSTs the gzip-compressed report to an https rua target as
// application/tlsrpt+gzip (RFC 8460 §3). The client is the SSRF-guarded one, so a
// target that resolves to a non-public address is refused at dial.
func (w *Worker) deliverReportHTTPS(uri string, jsonBytes []byte) error {
	gz, err := gzipBytes(jsonBytes)
	if err != nil {
		return err
	}
	client := w.TLSHTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Post(uri, "application/tlsrpt+gzip", bytes.NewReader(gz))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("tlsrpt: report POST to %s returned %d", uri, resp.StatusCode)
	}
	return nil
}

// deliverReportMail queues an RFC 8460 report email to a mailto rua target through
// the relay spool, so it goes out the ordinary external delivery path.
func (w *Worker) deliverReportMail(uri, policyDomain string, report *tlsrpt.Report, jsonBytes []byte) error {
	addr := strings.TrimPrefix(uri, "mailto:")
	if i := strings.IndexByte(addr, '?'); i >= 0 {
		addr = addr[:i] // drop any mailto query parameters
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("tlsrpt: empty mailto target %q", uri)
	}
	gz, err := gzipBytes(jsonBytes)
	if err != nil {
		return err
	}
	raw, err := buildReportMail(w.ReportDomain, addr, policyDomain, report, gz)
	if err != nil {
		return err
	}
	return w.Spool.Enqueue("tlsrpt-noreply@"+w.ReportDomain, []string{addr}, raw, time.Now())
}

// buildReportMail renders the RFC 8460 §3 report email: a multipart/mixed message
// carrying a short human-readable note and the gzip-compressed report as an
// application/tlsrpt+gzip attachment named per the RFC's convention.
func buildReportMail(fromDomain, toAddr, policyDomain string, r *tlsrpt.Report, gz []byte) ([]byte, error) {
	from := "tlsrpt-noreply@" + fromDomain
	filename := fmt.Sprintf("%s!%s!%d!%d.json.gz", fromDomain, policyDomain, r.DateRange.Start.Unix(), r.DateRange.End.Unix())

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	var h bytes.Buffer
	fmt.Fprintf(&h, "From: TLS Report <%s>\r\n", from)
	fmt.Fprintf(&h, "To: %s\r\n", toAddr)
	subject := fmt.Sprintf("Report Domain: %s Submitter: %s Report-ID: %s", policyDomain, fromDomain, r.ReportID)
	fmt.Fprintf(&h, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&h, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&h, "Message-ID: <%s>\r\n", r.ReportID)
	h.WriteString("MIME-Version: 1.0\r\n")
	h.WriteString("Auto-Submitted: auto-generated\r\n")
	fmt.Fprintf(&h, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mw.Boundary())

	note, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}})
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(note, "This is an aggregate TLS report from %s for %s.\r\n", fromDomain, policyDomain); err != nil {
		return nil, err
	}

	att, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"application/tlsrpt+gzip"},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {fmt.Sprintf("attachment; filename=%q", filename)},
	})
	if err != nil {
		return nil, err
	}
	if err := writeBase64(att, gz); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return append(h.Bytes(), body.Bytes()...), nil
}

// gzipBytes returns data gzip-compressed, the on-the-wire form of a TLS-RPT report.
func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeBase64 writes data base64-encoded and wrapped to 76-column lines, the MIME
// transfer encoding the attachment part declares.
func writeBase64(w interface{ Write([]byte) (int, error) }, data []byte) error {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 0 {
		n := min(76, len(enc))
		if _, err := fmt.Fprintf(w, "%s\r\n", enc[:n]); err != nil {
			return err
		}
		enc = enc[n:]
	}
	return nil
}

// logReport records a per-domain report-dispatch failure. The daily pass keeps going
// for the other domains, so this line is the operator's only signal that a domain's
// report could not be built, discovered, or delivered.
func (w *Worker) logReport(name, domain string, err error) {
	if w.Logger == nil {
		return
	}
	w.Logger.Emit(logging.Event{
		Level: logging.LevelError, Subsystem: logging.MTA, Name: name,
		Fields: logging.Fields{"domain": domain}, Err: err.Error(),
	})
}
