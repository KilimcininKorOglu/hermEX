package mta

import (
	"errors"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mailreport"
	"hermex/internal/tlsrpt"
)

// ReportRecorder stores the machine reports other servers send to a hosted
// domain's postmaster address, for the admin panel's report pages.
// *directory.SQLDirectory satisfies it; it is an interface so the MTA can be
// exercised without a database. Recording is fail-open.
type ReportRecorder interface {
	DomainID(domain string) (int64, bool, error)
	StoreDMARCAggregate(directory.ReportSource, *mailreport.Aggregate) (int64, error)
	StoreTLSReport(directory.ReportSource, *tlsrpt.Report) (int64, error)
	StoreDMARCFailure(directory.ReportSource, *mailreport.Failure) (int64, error)
}

// reportMailbox is the local part the DNS records the admin panel prescribes name
// as the reporting address (rua=mailto:postmaster@<domain>).
const reportMailbox = "postmaster"

// ingestReports stores the report an inbound message to a postmaster address
// carries. It runs after the message is filed and relayed, so nothing here can
// change what the sender was told: every outcome is recorded and swallowed.
//
// A report is accepted only for the domain it was addressed to. Anyone can send
// mail to a postmaster address, and a report naming a different domain would put
// the sender's numbers on that domain's page.
//
// sc is the message's spam scoring. Its DKIM result is stored with the report, so
// the panel can show whether the reporter signed the message; an unscored message
// stores no result rather than a failure.
func (s *session) ingestReports(raw []byte, received time.Time, sc scoring) {
	if s.reports == nil || s.authUser != "" {
		return
	}
	domains := s.postmasterDomains()
	if len(domains) == 0 {
		return
	}
	res, err := mailreport.Extract(raw)
	if errors.Is(err, mailreport.ErrNotReport) {
		return
	}
	if err != nil {
		s.emitReport(logging.LevelWarn, "report.parse_failed", "", logging.Fields{}, err)
		return
	}
	domain, ok := reportDomain(res, domains)
	if !ok {
		s.emitReport(logging.LevelWarn, "report.domain_mismatch", "",
			logging.Fields{"kind": string(res.Kind), "report_domains": strings.Join(res.Domains(), ",")}, nil)
		return
	}
	src := directory.ReportSource{ReceivedAt: received.Unix(), Via: directory.ViaMail, MailFrom: s.from, RemoteAddr: s.remoteAddr}
	if sc.scored {
		src.SenderDKIM, src.SenderDKIMDomains = string(sc.verdict.DKIM), sc.verdict.DKIMDomains
	}
	s.storeReport(res, domain, src)
}

// postmasterDomains returns the domains whose postmaster address the message was
// delivered to, lowercased.
func (s *session) postmasterDomains() map[string]bool {
	out := map[string]bool{}
	for _, t := range s.targets {
		at := strings.LastIndexByte(t.addr, '@')
		if at > 0 && strings.EqualFold(t.addr[:at], reportMailbox) {
			out[directory.AddressDomain(t.addr)] = true
		}
	}
	return out
}

// reportDomain returns the one domain the report speaks for, when that domain is
// one the message was addressed to.
func reportDomain(res mailreport.Result, addressed map[string]bool) (string, bool) {
	domain, ok := soleDomain(res)
	return domain, ok && addressed[domain]
}

// soleDomain returns the one domain the report speaks for. A report that names no
// domain, or several different ones, speaks for none.
func soleDomain(res mailreport.Result) (string, bool) {
	names := res.Domains()
	if len(names) == 0 || names[0] == "" {
		return "", false
	}
	for _, n := range names[1:] {
		if n != names[0] {
			return "", false
		}
	}
	return names[0], true
}

// storeOutcome is what storing one report came to.
type storeOutcome int

const (
	reportStored storeOutcome = iota
	reportDuplicate
	reportNotHosted
	reportStoreFailed
)

type storeResult struct {
	outcome storeOutcome
	id      int64
}

// storeReport writes the report under its domain. reportNotHosted means the
// domain is not one this server hosts; err is set for it and for a failed store.
func storeReport(rec ReportRecorder, res mailreport.Result, domain string, src directory.ReportSource) (storeResult, error) {
	domID, found, err := rec.DomainID(domain)
	if err != nil {
		return storeResult{outcome: reportStoreFailed}, err
	}
	if !found {
		return storeResult{outcome: reportNotHosted}, notHosted(domain)
	}
	src.DomainID = domID
	var id int64
	switch res.Kind {
	case mailreport.KindDMARCAggregate:
		id, err = rec.StoreDMARCAggregate(src, res.Aggregate)
	case mailreport.KindTLS:
		id, err = rec.StoreTLSReport(src, res.TLS)
	case mailreport.KindDMARCFailure:
		id, err = rec.StoreDMARCFailure(src, res.Failure)
	}
	switch {
	case errors.Is(err, directory.ErrDuplicateReport):
		return storeResult{outcome: reportDuplicate}, nil
	case err != nil:
		return storeResult{outcome: reportStoreFailed}, err
	}
	return storeResult{outcome: reportStored, id: id}, nil
}

// storeReport writes the report under its domain and records the outcome.
func (s *session) storeReport(res mailreport.Result, domain string, src directory.ReportSource) {
	fields := logging.Fields{"kind": string(res.Kind), "report_id": res.ReportID()}
	sr, err := storeReport(s.reports, res, domain, src)
	switch sr.outcome {
	case reportDuplicate:
		s.emitReport(logging.LevelInfo, "report.duplicate", domain, fields, nil)
	case reportNotHosted, reportStoreFailed:
		s.emitReport(logging.LevelError, "report.store_failed", domain, fields, err)
	default:
		fields["id"] = sr.id
		s.emitReport(logging.LevelInfo, "report.stored", domain, fields, nil)
	}
}

// notHosted names a report domain this server does not host.
func notHosted(domain string) error {
	return errors.New("the domain " + domain + " is not hosted here")
}

// emitReport records one report outcome. User names the postmaster address the
// report arrived at, so the log viewer can filter one domain's reports.
func (s *session) emitReport(level logging.Level, name, domain string, fields logging.Fields, err error) {
	fields["from"] = s.from
	e := logging.Event{Level: level, Subsystem: logging.MTA, Name: name, RemoteAddr: s.remoteAddr, Fields: fields}
	if domain != "" {
		e.User = reportMailbox + "@" + domain
	}
	if err != nil {
		e.Err = err.Error()
	}
	s.logger.Emit(e)
}
