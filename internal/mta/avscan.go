package mta

import (
	"bytes"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"hermex/internal/antivirus"
	"hermex/internal/directory"
	"hermex/internal/logging"
)

// avMaxScan caps the bytes streamed to clamd; it matches clamd's default
// StreamMaxLength, so a larger message is passed unscanned rather than tripping
// an INSTREAM size-limit error.
const avMaxScan = 25 * 1024 * 1024

// avMode selects the gating and the clamd-down policy at a delivery point.
type avMode int

const (
	// avInboundSMTP is unauthenticated MX intake: gate on the recipient domains'
	// inbound toggle; an unreachable clamd temp-fails (the sender retries).
	avInboundSMTP avMode = iota
	// avSubmission is authenticated submission (SMTP MSA, webmail, EWS, ROP): gate
	// on the sender domain's outbound toggle OR any recipient's inbound toggle
	// (local->local); an unreachable clamd fails open.
	avSubmission
	// avFetchmail is inbound external retrieval: gate on the recipient domain's
	// inbound toggle; an unreachable clamd fails open (no SMTP sender to defer).
	avFetchmail
)

// avDecision is what the caller should do after a scan attempt.
type avDecision int

const (
	avProceed  avDecision = iota // not scanned or clean: deliver normally
	avHandled                    // quarantined: skip delivery
	avTempFail                   // clamd unreachable on inbound SMTP: caller returns a 451
)

// avDirectory is the directory capability the scanner needs. *directory.SQLDirectory
// satisfies it; injected via SetScanner so the mail path is fail-closed-absent
// (no scanner configured means no scanning).
type avDirectory interface {
	GetDomainAVScan(domain string) (inbound, outbound bool, err error)
	DomainID(domain string) (int64, bool, error)
	QuarantineMessage(directory.QuarantineEntry) (int64, error)
	DomainOrgAdminEmails(domainID int64) ([]string, error)
}

type avConfig struct {
	scanner  *antivirus.Scanner
	dir      avDirectory
	quarPath func(int64) string
	hostname string
	logger   *logging.Logger
	maxScan  int
}

var avCtx atomic.Pointer[avConfig]

// SetScanner installs (or clears, with a nil scanner or dir) the package-level
// antivirus scanner the delivery paths consult. A daemon calls it once at startup
// from its clamd_addr config; with no scanner set, scanMessage always proceeds.
func SetScanner(scanner *antivirus.Scanner, dir avDirectory, quarPath func(int64) string, hostname string, logger *logging.Logger) {
	if scanner == nil || dir == nil || quarPath == nil {
		avCtx.Store(nil)
		return
	}
	avCtx.Store(&avConfig{scanner: scanner, dir: dir, quarPath: quarPath, hostname: hostname, logger: logger, maxScan: avMaxScan})
}

// scanMessage scans raw at a delivery point and reports what the caller should do.
// from is the envelope sender, recipients the envelope recipients. On a virus hit
// it quarantines the message, writes the raw bytes to disk, and notifies the
// affected party plus the domain/org admins, then returns avHandled so the caller
// skips delivery.
func scanMessage(accounts directory.Accounts, mode avMode, from string, recipients []string, raw []byte, when time.Time) avDecision {
	av := avCtx.Load()
	if av == nil {
		return avProceed
	}

	scope, direction, scanOn := av.gate(mode, from, recipients)
	if !scanOn {
		return avProceed
	}

	if len(raw) > av.maxScan {
		av.emit(logging.LevelWarn, "av.skip.toolarge", from, logging.Fields{"bytes": len(raw)})
		return avProceed
	}

	res, err := av.scanner.Scan(raw)
	if err != nil {
		av.emit(logging.LevelError, "av.scanner.unavailable", from, logging.Fields{"err": err.Error()})
		if mode == avInboundSMTP {
			return avTempFail
		}
		return avProceed // authenticated submission and fetchmail fail open
	}
	if res.Clean {
		return avProceed
	}

	entry := directory.QuarantineEntry{
		Direction:  direction,
		MailFrom:   from,
		Recipients: recipients,
		Subject:    subjectOf(raw),
		VirusName:  res.VirusName,
		DomainID:   scope,
		CreatedAt:  when.Unix(),
	}
	id, qerr := av.dir.QuarantineMessage(entry)
	if qerr != nil {
		av.emit(logging.LevelError, "av.quarantine.fail", from, logging.Fields{"virus": res.VirusName, "err": qerr.Error()})
		// Never deliver a known virus: defer inbound (retry), drop authenticated.
		if mode == avInboundSMTP {
			return avTempFail
		}
		return avHandled
	}
	if werr := writeQuarantineEml(av.quarPath(id), raw); werr != nil {
		av.emit(logging.LevelError, "av.quarantine.eml.fail", from, logging.Fields{"id": id, "err": werr.Error()})
	}
	admins, _ := av.dir.DomainOrgAdminEmails(scope)
	affected := recipients
	if direction == "outbound" {
		affected = []string{from}
	}
	notifyQuarantine(accounts, directory.QuarantineRecord{ID: id, QuarantineEntry: entry, Status: "held"}, affected, admins, av.hostname, when)
	av.emit(logging.LevelWarn, "av.quarantined", from, logging.Fields{"id": id, "virus": res.VirusName, "direction": direction})
	return avHandled
}

// gate decides whether to scan and, if so, the scoping domain id and the
// direction label, applying the per-mode toggle rules.
func (av *avConfig) gate(mode avMode, from string, recipients []string) (scope int64, direction string, on bool) {
	if mode == avSubmission {
		if _, out, id, ok := av.flags(domainOf(from)); ok && out {
			return id, "outbound", true
		}
	}
	// Both inbound modes, and the local->local leg of submission, gate on a
	// recipient domain whose inbound toggle is set.
	for _, rcpt := range recipients {
		if in, _, id, ok := av.flags(domainOf(rcpt)); ok && in {
			return id, "inbound", true
		}
	}
	return 0, "", false
}

// flags resolves a domain's scan toggles and id, reporting ok=false when the
// domain is unknown or both toggles are off (so a lookup error fails open).
func (av *avConfig) flags(domain string) (inbound, outbound bool, id int64, ok bool) {
	in, out, err := av.dir.GetDomainAVScan(domain)
	if err != nil || (!in && !out) {
		return false, false, 0, false
	}
	id, found, err := av.dir.DomainID(domain)
	if err != nil || !found {
		return false, false, 0, false
	}
	return in, out, id, true
}

func (av *avConfig) emit(level logging.Level, name, from string, f logging.Fields) {
	if av.logger == nil {
		return
	}
	if f == nil {
		f = logging.Fields{}
	}
	f["from"] = from
	av.logger.Emit(logging.Event{Level: level, Subsystem: logging.MTA, Name: name, Fields: f})
}

// subjectOf extracts the decoded Subject header from a raw message (empty on a
// parse failure), for the quarantine record and notice.
func subjectOf(raw []byte) string {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	subj := m.Header.Get("Subject")
	if dec, err := (&mime.WordDecoder{}).DecodeHeader(subj); err == nil {
		return dec
	}
	return subj
}

// writeQuarantineEml writes raw to path atomically (temp file then rename) so a
// reader never sees a partial quarantine message.
func writeQuarantineEml(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// avRecipients flattens a session's local and relay recipients into one list for
// the scanner.
func avRecipients(local []target, relay []string) []string {
	out := make([]string, 0, len(local)+len(relay))
	for _, t := range local {
		out = append(out, t.addr)
	}
	return append(out, relay...)
}
