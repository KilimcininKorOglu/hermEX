package mta

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mailreport"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/tlsrpt"
)

// fakeReports is a scripted ReportRecorder that records what it was asked to store.
type fakeReports struct {
	mu      sync.Mutex
	domains map[string]int64
	err     error
	stored  []directory.ReportSource
}

func (f *fakeReports) DomainID(domain string) (int64, bool, error) {
	id, ok := f.domains[domain]
	return id, ok, nil
}

func (f *fakeReports) record(src directory.ReportSource) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	f.stored = append(f.stored, src)
	return int64(len(f.stored)), nil
}

func (f *fakeReports) StoreDMARCAggregate(src directory.ReportSource, _ *mailreport.Aggregate) (int64, error) {
	return f.record(src)
}

func (f *fakeReports) StoreTLSReport(src directory.ReportSource, _ *tlsrpt.Report) (int64, error) {
	return f.record(src)
}

func (f *fakeReports) StoreDMARCFailure(src directory.ReportSource, _ *mailreport.Failure) (int64, error) {
	return f.record(src)
}

// tlsReportMail builds a TLS report message naming domain as its policy domain.
func tlsReportMail(domain string) string {
	return "From: tls@reporter.example\r\nSubject: Report\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: application/tlsrpt+json\r\n\r\n" +
		`{"organization-name":"Reporter","date-range":{"start-datetime":"2025-09-19T00:00:00Z","end-datetime":"2025-09-19T23:59:59Z"},` +
		`"contact-info":"tls@reporter.example","report-id":"r-1","policies":[{"policy":{"policy-type":"sts","policy-domain":"` + domain + `"},` +
		`"summary":{"total-successful-session-count":5,"total-failure-session-count":0}}]}` + "\r\n"
}

// reportRig provisions alice's mailbox, with postmaster@hermex.test an alias of it,
// and a backend that records reports and events.
type reportRig struct {
	mbox    string
	reports *fakeReports
	sink    *captureSink
	backend *Backend
}

func newReportRig(t *testing.T) *reportRig {
	t.Helper()
	mbox := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(mbox)
	mustNoErr(t, "open the mailbox", err)
	st.Close()
	accounts := directory.StaticAccounts{
		"alice@hermex.test":      {MailboxPath: mbox},
		"postmaster@hermex.test": {MailboxPath: mbox},
	}
	r := &reportRig{mbox: mbox, reports: &fakeReports{domains: map[string]int64{"hermex.test": 7}}, sink: &captureSink{}}
	r.backend = &Backend{Accounts: accounts, Logger: logging.New(r.sink), Reports: r.reports}
	return r
}

func (r *reportRig) eventNamed(name string) (logging.Event, bool) {
	r.sink.mu.Lock()
	defer r.sink.mu.Unlock()
	return findEvent(r.sink.events, name)
}

// TestAReportToPostmasterIsDeliveredAndStored is the feature: the report still
// lands in the postmaster mailbox, and it is stored under its domain as well.
func TestAReportToPostmasterIsDeliveredAndStored(t *testing.T) {
	r := newReportRig(t)
	deliverBody(t, r.backend, "198.51.100.1:25", "tls@reporter.example", "postmaster@hermex.test", tlsReportMail("hermex.test"))

	wantEq(t, "messages delivered to postmaster", len(folderMessages(t, r.mbox, int64(mapi.PrivateFIDInbox))), 1)
	if len(r.reports.stored) != 1 {
		t.Fatalf("stored reports = %d, want 1", len(r.reports.stored))
	}
	src := r.reports.stored[0]
	wantEq(t, "the stored domain id", src.DomainID, int64(7))
	wantEq(t, "the stored sender", src.MailFrom, "tls@reporter.example")
	wantEq(t, "the stored client address", src.RemoteAddr, "198.51.100.1:25")
	e, ok := r.eventNamed("report.stored")
	if !ok {
		t.Fatal("no report.stored event")
	}
	wantEq(t, "the event's mailbox", e.User, "postmaster@hermex.test")
}

// TestAReportToAnotherRecipientIsNotStored keeps ordinary mailboxes out of it: a
// report is read only at the address the DNS records name.
func TestAReportToAnotherRecipientIsNotStored(t *testing.T) {
	r := newReportRig(t)
	deliverBody(t, r.backend, "198.51.100.1:25", "tls@reporter.example", "alice@hermex.test", tlsReportMail("hermex.test"))
	wantEq(t, "stored reports", len(r.reports.stored), 0)
}

// TestAReportForAnotherDomainIsNotStored is the forgery guard: anyone can mail a
// postmaster address, and a report naming another domain must not reach that
// domain's page.
func TestAReportForAnotherDomainIsNotStored(t *testing.T) {
	r := newReportRig(t)
	r.reports.domains["victim.test"] = 9
	deliverBody(t, r.backend, "198.51.100.1:25", "tls@reporter.example", "postmaster@hermex.test", tlsReportMail("victim.test"))

	wantEq(t, "stored reports", len(r.reports.stored), 0)
	if _, ok := r.eventNamed("report.domain_mismatch"); !ok {
		t.Error("no report.domain_mismatch event")
	}
}

// TestAReportStoreFailureLeavesTheDeliveryAlone holds the fail-open rule: the
// message is already filed when the report is stored, so a store failure is
// recorded and the sender is still told the message was accepted.
func TestAReportStoreFailureLeavesTheDeliveryAlone(t *testing.T) {
	r := newReportRig(t)
	r.reports.err = errors.New("the database is gone")
	deliverBody(t, r.backend, "198.51.100.1:25", "tls@reporter.example", "postmaster@hermex.test", tlsReportMail("hermex.test"))

	wantEq(t, "messages delivered to postmaster", len(folderMessages(t, r.mbox, int64(mapi.PrivateFIDInbox))), 1)
	e, ok := r.eventNamed("report.store_failed")
	if !ok {
		t.Fatal("no report.store_failed event")
	}
	wantEq(t, "the recorded error", e.Err, "the database is gone")
}

// TestADuplicateReportIsNotAFailure: reporters resend reports, and the second copy
// is expected, not an error to alert on.
func TestADuplicateReportIsNotAFailure(t *testing.T) {
	r := newReportRig(t)
	r.reports.err = directory.ErrDuplicateReport
	deliverBody(t, r.backend, "198.51.100.1:25", "tls@reporter.example", "postmaster@hermex.test", tlsReportMail("hermex.test"))

	if _, ok := r.eventNamed("report.duplicate"); !ok {
		t.Error("no report.duplicate event")
	}
	if _, ok := r.eventNamed("report.store_failed"); ok {
		t.Error("a duplicate report was recorded as a store failure")
	}
}
