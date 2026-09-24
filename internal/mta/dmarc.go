package mta

import (
	"net"
	"strings"
	"time"

	"hermex/internal/antispam"
	"hermex/internal/logging"
	"hermex/internal/mailreport"
	"hermex/internal/relay"
)

// DMARCRecorder counts one inbound message's DMARC evaluation for the daily
// aggregate report (RFC 7489 §7.2) to its From domain. *relay.Spool satisfies it;
// the MTA wraps it with the operator's reporting switch. Recording is fail-open.
type DMARCRecorder interface {
	RecordDMARC(now time.Time, o relay.DMARCObservation) error
}

// maxReportedDomain bounds a domain name a report row carries. A DNS name is at
// most 253 octets; a failed signature's d= tag is whatever the sender wrote.
const maxReportedDomain = 253

// recordDMARC counts the message for its From domain's aggregate report. Only a
// domain that publishes a record asking for reports is counted, and only a message
// from a known client address, since a report row names its source. A failed write
// is logged and never changes the delivery.
func (s *session) recordDMARC(sc scoring, ip net.IP, received time.Time) {
	v := sc.verdict
	if s.dmarc == nil || !v.DMARCReports || ip == nil || sc.fromDom == "" {
		return
	}
	policyDomain := capDomain(strings.ToLower(sc.fromDom))
	envDomain := capDomain(senderDomain(s.from))
	o := relay.DMARCObservation{
		PolicyDomain: policyDomain,
		SourceIP:     ip.String(),
		EnvelopeFrom: envDomain,
		DKIM:         passOrFail(v.DMARCDKIMAligned),
		SPF:          passOrFail(v.DMARCSPFAligned),
		Disposition:  dmarcDisposition(v),
		DKIMResults:  dkimAuthResults(v.DKIMResults),
		SPFResults:   []mailreport.AuthResult{spfAuthResult(envDomain, policyDomain, v.SPF)},
	}
	if err := s.dmarc.RecordDMARC(received, o); err != nil && s.logger != nil {
		s.logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "dmarc.record.fail", RemoteAddr: s.remoteAddr, Fields: logging.Fields{"domain": policyDomain}, Err: err.Error()})
	}
}

// dmarcDisposition is what DMARC made of the message. This server never rejects on
// DMARC at the SMTP level; a failure under an enforcing policy adds its weight to
// the spam score, so it reads as quarantined when that filed the message to Junk.
func dmarcDisposition(v antispam.Verdict) string {
	if v.DMARCReject && v.Spam {
		return "quarantine"
	}
	return "none"
}

// dkimAuthResults lists every checked signature's domain and outcome.
func dkimAuthResults(in []antispam.DKIMResult) []mailreport.AuthResult {
	out := make([]mailreport.AuthResult, 0, len(in))
	for _, d := range in {
		out = append(out, mailreport.AuthResult{Domain: capDomain(strings.ToLower(d.Domain)), Result: passOrFail(d.Valid)})
	}
	return out
}

// spfAuthResult is the raw SPF result for the envelope sender's domain. A null
// sender was not checked, so it is reported as none against the From domain.
func spfAuthResult(envDomain, policyDomain string, spf antispam.AuthResult) mailreport.AuthResult {
	if envDomain == "" {
		return mailreport.AuthResult{Domain: policyDomain, Result: "none"}
	}
	result := string(spf)
	if spf == antispam.AuthError {
		result = "temperror" // the report schema's name for an unresolved check
	}
	return mailreport.AuthResult{Domain: envDomain, Result: result}
}

// senderDomain returns the lowercased domain of an envelope sender, "" for a null
// sender or one without a domain.
func senderDomain(addr string) string {
	_, dom, ok := strings.Cut(strings.ToLower(strings.TrimSpace(addr)), "@")
	if !ok {
		return ""
	}
	return dom
}

// capDomain bounds a domain name to maxReportedDomain bytes.
func capDomain(d string) string {
	if len(d) > maxReportedDomain {
		return d[:maxReportedDomain]
	}
	return d
}

func passOrFail(ok bool) string {
	if ok {
		return "pass"
	}
	return "fail"
}
