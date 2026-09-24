package relay

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-msgauth/dmarc"
	"golang.org/x/net/publicsuffix"

	"hermex/internal/logging"
	"hermex/internal/mailreport"
)

// dailyDMARCReports sends the given day's DMARC aggregate reports while the
// operator setting is on, then prunes counters older than cutoff whatever the
// setting says, so rows recorded before it was switched off do not linger.
func (w *Worker) dailyDMARCReports(ctx context.Context, day, cutoff time.Time) {
	if w.DMARCEnabled == nil || w.DMARCEnabled() {
		if err := w.sendDMARCReports(ctx, day); err != nil {
			w.logPass("dmarc.report.fail", err)
		}
	}
	if err := w.Spool.PruneDMARCReports(cutoff); err != nil {
		w.logPass("dmarc.prune.fail", err)
	}
}

// sendDMARCReports dispatches the DMARC aggregate report (RFC 7489 §7.2) of every
// policy domain that recorded counters on the given UTC day and has not been
// reported yet. One domain's failure never stops the others.
func (w *Worker) sendDMARCReports(ctx context.Context, day time.Time) error {
	domains, err := w.Spool.UnreportedDMARCDomains(day)
	if err != nil {
		return err
	}
	for _, domain := range domains {
		if ctx.Err() != nil {
			return nil
		}
		w.reportDMARCDomain(day, domain)
	}
	return nil
}

// reportDMARCDomain builds and sends one policy domain's daily report. The domain
// is marked reported on every outcome, so a failure loses that day's report rather
// than re-looping the pass; every failure is logged.
func (w *Worker) reportDMARCDomain(day time.Time, domain string) {
	defer func() {
		if err := w.Spool.MarkDMARCReported(day, domain, time.Now()); err != nil {
			w.logReport("dmarc.mark.fail", domain, err)
		}
	}()
	agg, ruas := w.assembleDMARCReport(day, domain)
	if agg == nil {
		return
	}
	doc, err := agg.XML()
	if err == nil {
		doc, err = gzipBytes(doc)
	}
	if err != nil {
		w.logReport("dmarc.encode.fail", domain, err)
		return
	}
	for _, uri := range ruas {
		if err := w.deliverDMARCReport(uri, domain, agg, doc); err != nil {
			w.logReport("dmarc.deliver.fail", domain, err)
		}
	}
}

// assembleDMARCReport returns one policy domain's report and the destinations its
// record names, or a nil report when there is nothing to send: no counters, no
// record, or no rua=. A read failure is logged.
func (w *Worker) assembleDMARCReport(day time.Time, domain string) (*mailreport.Aggregate, []string) {
	records, err := w.Spool.DMARCRecords(day, domain)
	if err != nil {
		w.logReport("dmarc.assemble.fail", domain, err)
		return nil, nil
	}
	if len(records) == 0 {
		return nil, nil
	}
	// The record is read again now rather than kept from delivery time: the report
	// states the policy it was evaluated against, and rua= says where it goes today.
	rec, err := w.DMARCLookup(domain)
	if err != nil {
		w.logReport("dmarc.discover.fail", domain, err)
		return nil, nil
	}
	if rec == nil || len(rec.ReportURIAggregate) == 0 {
		return nil, nil
	}
	return w.dmarcAggregate(day, domain, rec, records), rec.ReportURIAggregate
}

// dmarcAggregate assembles the report document for one policy domain and day. An
// alignment mode or percentage the record leaves out is reported at its RFC 7489
// default (relaxed, 100), which is what the evaluation applied.
func (w *Worker) dmarcAggregate(day time.Time, domain string, rec *dmarc.Record, records []mailreport.Record) *mailreport.Aggregate {
	d := day.UTC().Truncate(24 * time.Hour)
	pct := "100"
	if rec.Percent != nil {
		pct = strconv.Itoa(*rec.Percent)
	}
	return &mailreport.Aggregate{
		OrgName:  w.ReportOrg,
		Email:    strings.TrimPrefix(w.ReportContact, "mailto:"),
		ReportID: fmt.Sprintf("%d.%s@%s", d.Unix(), domain, w.ReportDomain),
		Begin:    d,
		End:      d.Add(24*time.Hour - time.Second),
		Domain:   domain,
		ADKIM:    alignmentOrRelaxed(rec.DKIMAlignment),
		ASPF:     alignmentOrRelaxed(rec.SPFAlignment),
		P:        string(rec.Policy),
		SP:       string(rec.SubdomainPolicy),
		Pct:      pct,
		Records:  records,
	}
}

// alignmentOrRelaxed returns the record's alignment mode, "r" when it names none.
func alignmentOrRelaxed(m dmarc.AlignmentMode) string {
	if m == "" {
		return string(dmarc.AlignmentRelaxed)
	}
	return string(m)
}

// dmarcTarget is one parsed rua= destination: the mailbox and the size limit its
// "!" suffix sets, 0 for none.
type dmarcTarget struct {
	addr  string
	limit int64
}

// deliverDMARCReport queues the report email to one rua= destination. Only mailto:
// destinations are served. A destination outside the policy domain's organization
// must have authorized reports for it (RFC 7489 §7.1), and a report larger than the
// destination's size limit is not sent.
func (w *Worker) deliverDMARCReport(uri, policyDomain string, agg *mailreport.Aggregate, gz []byte) error {
	target, err := parseDMARCURI(uri)
	if err != nil {
		return err
	}
	if err := w.dmarcDestinationAllowed(policyDomain, target.addr); err != nil {
		return err
	}
	raw, err := buildDMARCReportMail(w.ReportDomain, target.addr, agg, gz)
	if err != nil {
		return err
	}
	if target.limit > 0 && int64(len(raw)) > target.limit {
		return fmt.Errorf("dmarc: report of %d bytes exceeds the %d-byte limit of %s", len(raw), target.limit, uri)
	}
	return w.Spool.Enqueue(dmarcReportSender+"@"+w.ReportDomain, []string{target.addr}, raw, time.Now())
}

// dmarcReportSender is the local part of the address DMARC reports are sent from.
const dmarcReportSender = "noreply"

// parseDMARCURI reads a rua= entry (RFC 7489 §6.2): a URI with an optional "!"
// size limit in bytes, which a k, m, g or t unit scales by powers of 1024.
func parseDMARCURI(uri string) (dmarcTarget, error) {
	const scheme = "mailto:"
	u, limit, _ := strings.Cut(strings.TrimSpace(uri), "!")
	if len(u) < len(scheme) || !strings.EqualFold(u[:len(scheme)], scheme) {
		return dmarcTarget{}, fmt.Errorf("dmarc: unsupported report URI %q", uri)
	}
	addr, _, _ := strings.Cut(u[len(scheme):], "?")
	addr, err := url.PathUnescape(addr)
	if err != nil {
		return dmarcTarget{}, fmt.Errorf("dmarc: report URI %q: %w", uri, err)
	}
	addr = strings.TrimSpace(addr)
	if local, domain, ok := strings.Cut(addr, "@"); !ok || local == "" || domain == "" {
		return dmarcTarget{}, fmt.Errorf("dmarc: report URI %q names no mailbox", uri)
	}
	n, err := parseDMARCSize(limit)
	if err != nil {
		return dmarcTarget{}, fmt.Errorf("dmarc: report URI %q: %w", uri, err)
	}
	return dmarcTarget{addr: addr, limit: n}, nil
}

// parseDMARCSize reads a "!" size limit; an empty one means no limit.
func parseDMARCSize(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	shift := strings.Index("kmgt", strings.ToLower(s[len(s)-1:])) + 1
	if shift > 0 {
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > (1<<62)>>(10*shift) {
		return 0, fmt.Errorf("invalid size limit %q", s)
	}
	return n << (10 * shift), nil
}

// errDestinationNotAuthorized reports a rua= destination outside the policy
// domain's organization that has not agreed to receive its reports.
var errDestinationNotAuthorized = errors.New("dmarc: report destination has not authorized reports for this domain")

// dmarcDestinationAllowed applies the external destination check (RFC 7489 §7.1):
// a destination in the policy domain's own organization is always allowed; any
// other must publish a "v=DMARC1" TXT record at
// <policy-domain>._report._dmarc.<destination-domain>. Without the check anyone
// could publish a record that makes this server mail reports to a third party.
func (w *Worker) dmarcDestinationAllowed(policyDomain, addr string) error {
	_, destDomain, _ := strings.Cut(strings.ToLower(addr), "@")
	if organization(destDomain) == organization(policyDomain) {
		return nil
	}
	if w.DMARCLookupTXT == nil {
		return errDestinationNotAuthorized
	}
	txts, err := w.DMARCLookupTXT(strings.ToLower(policyDomain) + "._report._dmarc." + destDomain)
	if err != nil {
		return fmt.Errorf("%w: %v", errDestinationNotAuthorized, err)
	}
	for _, t := range txts {
		v, _, _ := strings.Cut(t, ";")
		if strings.EqualFold(strings.ReplaceAll(v, " ", ""), "v=DMARC1") {
			return nil
		}
	}
	return errDestinationNotAuthorized
}

// organization returns a domain's organizational domain (eTLD+1), falling back to
// the name itself when the public suffix list cannot place it.
func organization(domain string) string {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if org, err := publicsuffix.EffectiveTLDPlusOne(d); err == nil {
		return org
	}
	return d
}

// buildDMARCReportMail renders the report email of RFC 7489 §7.2.1.1: the fixed
// subject form, and the gzip-compressed report as an application/gzip attachment
// named receiver!policy-domain!begin!end.xml.gz.
func buildDMARCReportMail(submitter, toAddr string, agg *mailreport.Aggregate, gz []byte) ([]byte, error) {
	return reportMail{
		from:        dmarcReportSender + "@" + submitter,
		fromName:    "DMARC Report",
		to:          toAddr,
		subject:     fmt.Sprintf("Report Domain: %s Submitter: %s Report-ID: <%s>", agg.Domain, submitter, agg.ReportID),
		messageID:   agg.ReportID,
		note:        fmt.Sprintf("This is a DMARC aggregate report from %s for %s.\r\n", submitter, agg.Domain),
		contentType: "application/gzip",
		filename:    fmt.Sprintf("%s!%s!%d!%d.xml.gz", submitter, agg.Domain, agg.Begin.Unix(), agg.End.Unix()),
		payload:     gz,
	}.build()
}

// logPass records a failure of a whole daily report pass.
func (w *Worker) logPass(name string, err error) {
	if w.Logger == nil {
		return
	}
	w.Logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: name, Err: err.Error()})
}
