package relay

import (
	"crypto/tls"
	"net"
	"testing"
	"time"

	"hermex/internal/mtasts"
	hsmtp "hermex/internal/smtp"
	"hermex/internal/tlsrpt"
	"hermex/internal/tlstest"
)

func record(t *testing.T, sp *Spool, day time.Time, domain, policyType, mx, result string) {
	t.Helper()
	if err := sp.RecordTLS(day, domain, policyType, mx, result); err != nil {
		t.Fatalf("RecordTLS: %v", err)
	}
}

// findPolicy returns the report policy for an mx host, failing if absent.
func findPolicy(t *testing.T, rep *tlsrpt.Report, mx string) tlsrpt.PolicyResult {
	t.Helper()
	for _, p := range rep.Policies {
		if p.Policy.MXHost == mx {
			return p
		}
	}
	t.Fatalf("report has no policy for mx %q: %+v", mx, rep.Policies)
	return tlsrpt.PolicyResult{}
}

// TestTLSReportAggregates proves the daily report sums recorded sessions per
// (policy-type, mail exchanger): repeated outcomes accumulate, a success and a
// failure to the same MX land in one policy with split counts and a failure
// detail, a second MX is a second policy, and the covered UTC day is rendered as
// the inclusive full-day range RFC 8460 §4.4 requires.
func TestTLSReportAggregates(t *testing.T) {
	sp := openSpool(t)
	day := time.Date(2026, 6, 27, 9, 30, 0, 0, time.UTC)

	record(t, sp, day, "remote.example", tlsrpt.PolicyTypeTLSA, "mx1.remote.example", "")
	record(t, sp, day, "remote.example", tlsrpt.PolicyTypeTLSA, "mx1.remote.example", "")
	record(t, sp, day, "remote.example", tlsrpt.PolicyTypeTLSA, "mx1.remote.example", tlsrpt.ResultCertificateExpired)
	record(t, sp, day, "remote.example", tlsrpt.PolicyTypeSTS, "mx2.remote.example", "")

	rep, err := sp.TLSReport(day, "remote.example", "hermEX", "tls@hermex.test", "rep-1")
	if err != nil {
		t.Fatalf("TLSReport: %v", err)
	}
	if rep == nil {
		t.Fatal("report is nil, want recorded sessions")
	}
	if rep.OrganizationName != "hermEX" || rep.ContactInfo != "tls@hermex.test" || rep.ReportID != "rep-1" {
		t.Errorf("report identity = %+v", rep)
	}
	if !rep.DateRange.Start.Equal(time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date-range start = %v, want midnight UTC of the covered day", rep.DateRange.Start)
	}
	if !rep.DateRange.End.Equal(time.Date(2026, 6, 27, 23, 59, 59, 0, time.UTC)) {
		t.Errorf("date-range end = %v, want 23:59:59 UTC", rep.DateRange.End)
	}
	if len(rep.Policies) != 2 {
		t.Fatalf("report has %d policies, want 2", len(rep.Policies))
	}

	mx1 := findPolicy(t, rep, "mx1.remote.example")
	if mx1.Policy.PolicyType != tlsrpt.PolicyTypeTLSA {
		t.Errorf("mx1 policy-type = %q, want tlsa", mx1.Policy.PolicyType)
	}
	if mx1.Summary.TotalSuccessful != 2 || mx1.Summary.TotalFailure != 1 {
		t.Errorf("mx1 summary = %+v, want 2 success 1 failure", mx1.Summary)
	}
	if len(mx1.FailureDetails) != 1 ||
		mx1.FailureDetails[0].ResultType != tlsrpt.ResultCertificateExpired ||
		mx1.FailureDetails[0].FailedSessionCount != 1 ||
		mx1.FailureDetails[0].ReceivingMXHostname != "mx1.remote.example" {
		t.Errorf("mx1 failure details = %+v", mx1.FailureDetails)
	}

	mx2 := findPolicy(t, rep, "mx2.remote.example")
	if mx2.Summary.TotalSuccessful != 1 || mx2.Summary.TotalFailure != 0 {
		t.Errorf("mx2 summary = %+v, want 1 success 0 failure", mx2.Summary)
	}
	if len(mx2.FailureDetails) != 0 {
		t.Errorf("mx2 has failure details with no failures: %+v", mx2.FailureDetails)
	}
}

// TestTLSReportEmpty proves a day or domain with no recorded sessions yields a
// nil report, so the caller skips an empty submission rather than send a report
// with no data.
func TestTLSReportEmpty(t *testing.T) {
	sp := openSpool(t)
	day := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	record(t, sp, day, "other.example", tlsrpt.PolicyTypeSTS, "mx.other.example", "")

	rep, err := sp.TLSReport(day, "remote.example", "org", "c", "id")
	if err != nil {
		t.Fatalf("TLSReport: %v", err)
	}
	if rep != nil {
		t.Errorf("report for a domain with no sessions = %+v, want nil", rep)
	}
}

// TestTLSReportDurableAcrossReopen proves the counters survive a spool reopen, so
// a full UTC day aggregates across MTA restarts rather than resetting.
func TestTLSReportDurableAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/relay.sqlite3"
	day := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)

	sp, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record(t, sp, day, "remote.example", tlsrpt.PolicyTypeSTS, "mx.remote.example", "")
	sp.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rep, err := reopened.TLSReport(day, "remote.example", "org", "c", "id")
	if err != nil {
		t.Fatalf("TLSReport after reopen: %v", err)
	}
	if rep == nil || len(rep.Policies) != 1 || rep.Policies[0].Summary.TotalSuccessful != 1 {
		t.Errorf("report after reopen = %+v, want one session preserved", rep)
	}
}

// startTLSSink is startSink with a self-signed certificate, so the server
// advertises and accepts STARTTLS (TLSConfig non-nil).
func startTLSSink(t *testing.T) (*recordingBackend, string) {
	t.Helper()
	be := &recordingBackend{}
	certPath, keyPath, err := tlstest.SelfSigned(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	srv := &hsmtp.Server{Backend: be, Hostname: "sink.test", TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	go srv.Serve(ln)
	return be, ln.Addr().String()
}

// TestWorkerRecordsTLSSuccess proves a real STARTTLS delivery records one
// successful session for the recipient domain through the worker's reporter, so
// the recorder is wired into send() and the success path counts. Opportunistic
// TLS (no policy) accepts any certificate, so the self-signed sink authenticates
// and the session is a no-policy-found success.
func TestWorkerRecordsTLSSuccess(t *testing.T) {
	_, addr := startTLSSink(t)
	sp := openSpool(t)
	now := time.Now()
	if err := sp.Enqueue("a@local", []string{"bob@remote"}, []byte("hi\r\n"), now); err != nil {
		t.Fatal(err)
	}
	w := &Worker{
		Spool:       sp,
		HeloName:    "mx.test",
		Router:      func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:      func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
		TLSReporter: sp,
	}
	if sent, err := w.ProcessDue(now); err != nil || sent != 1 {
		t.Fatalf("process: sent=%d err=%v", sent, err)
	}
	rep, err := sp.TLSReport(now, "remote", "org", "c", "id")
	if err != nil {
		t.Fatalf("TLSReport: %v", err)
	}
	if rep == nil || len(rep.Policies) != 1 {
		t.Fatalf("report = %+v, want one policy", rep)
	}
	p := rep.Policies[0]
	if p.Policy.PolicyType != tlsrpt.PolicyTypeNoPolicy {
		t.Errorf("policy-type = %q, want no-policy-found", p.Policy.PolicyType)
	}
	if p.Summary.TotalSuccessful != 1 || p.Summary.TotalFailure != 0 {
		t.Errorf("summary = %+v, want 1 success 0 failure", p.Summary)
	}
}

// TestWorkerRecordsStartTLSNotSupported proves that when an MTA-STS enforce policy
// requires TLS but the listed MX offers no STARTTLS, the refused delivery records
// a starttls-not-supported failure under the sts policy type, the negotiation
// failure TLS-RPT exists to surface.
func TestWorkerRecordsStartTLSNotSupported(t *testing.T) {
	_, addr := startSink(t) // plain sink: no STARTTLS
	sp := openSpool(t)
	now := time.Now()
	if err := sp.Enqueue("a@local", []string{"bob@remote"}, []byte("hi\r\n"), now); err != nil {
		t.Fatal(err)
	}
	w := &Worker{
		Spool:       sp,
		Router:      func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:      func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
		TLSReporter: sp,
		Policy: func(string) (*mtasts.Policy, error) {
			return &mtasts.Policy{Mode: mtasts.ModeEnforce, MX: []string{"sink"}}, nil
		},
	}
	if _, err := w.ProcessDue(now); err != nil {
		t.Fatalf("process: %v", err)
	}
	rep, err := sp.TLSReport(now, "remote", "org", "c", "id")
	if err != nil {
		t.Fatalf("TLSReport: %v", err)
	}
	if rep == nil || len(rep.Policies) != 1 {
		t.Fatalf("report = %+v, want one policy", rep)
	}
	p := rep.Policies[0]
	if p.Policy.PolicyType != tlsrpt.PolicyTypeSTS {
		t.Errorf("policy-type = %q, want sts", p.Policy.PolicyType)
	}
	if p.Summary.TotalFailure != 1 || len(p.FailureDetails) != 1 ||
		p.FailureDetails[0].ResultType != tlsrpt.ResultStartTLSNotSupported {
		t.Errorf("expected one starttls-not-supported failure, got %+v", p)
	}
}

// TestWorkerOpportunisticPlaintextNotRecorded proves a cleartext delivery with no
// policy and no STARTTLS records nothing: TLS-RPT reports TLS sessions, and a
// legacy plaintext hop to a domain that published no policy has no report to
// pollute.
func TestWorkerOpportunisticPlaintextNotRecorded(t *testing.T) {
	_, addr := startSink(t) // plain sink: no STARTTLS
	sp := openSpool(t)
	now := time.Now()
	if err := sp.Enqueue("a@local", []string{"bob@remote"}, []byte("hi\r\n"), now); err != nil {
		t.Fatal(err)
	}
	w := &Worker{
		Spool:       sp,
		Router:      func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:      func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
		TLSReporter: sp,
	}
	if sent, err := w.ProcessDue(now); err != nil || sent != 1 {
		t.Fatalf("process: sent=%d err=%v", sent, err)
	}
	rep, err := sp.TLSReport(now, "remote", "org", "c", "id")
	if err != nil {
		t.Fatalf("TLSReport: %v", err)
	}
	if rep != nil {
		t.Errorf("plaintext delivery recorded a TLS session: %+v", rep)
	}
}
