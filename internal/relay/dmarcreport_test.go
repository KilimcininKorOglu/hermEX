package relay

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dmarc"

	"hermex/internal/logging"
	"hermex/internal/mailreport"
	"hermex/internal/mime"
)

// seedDMARC records three messages for example.test: two identical ones that
// share a row and one from another source.
func seedDMARC(t *testing.T, sp *Spool) {
	t.Helper()
	pass := DMARCObservation{
		PolicyDomain: "example.test", SourceIP: "198.51.100.7", EnvelopeFrom: "example.test",
		DKIM: "pass", SPF: "pass", Disposition: "none",
		DKIMResults: []mailreport.AuthResult{{Domain: "example.test", Result: "pass"}},
		SPFResults:  []mailreport.AuthResult{{Domain: "example.test", Result: "pass"}},
	}
	fail := DMARCObservation{
		PolicyDomain: "example.test", SourceIP: "203.0.113.9", EnvelopeFrom: "spoof.test",
		DKIM: "fail", SPF: "fail", Disposition: "quarantine",
		SPFResults: []mailreport.AuthResult{{Domain: "spoof.test", Result: "fail"}},
	}
	for _, o := range []DMARCObservation{pass, pass, fail} {
		if err := sp.RecordDMARC(reportDay, o); err != nil {
			t.Fatal(err)
		}
	}
}

// dmarcRecord returns a lookup that answers every domain with the given record.
func dmarcRecord(txt string) func(string) (*dmarc.Record, error) {
	return func(string) (*dmarc.Record, error) { return dmarc.Parse(txt) }
}

// dmarcWorker is a worker wired for DMARC reporting only.
func dmarcWorker(sp *Spool, record string) *Worker {
	return &Worker{
		Spool:         sp,
		ReportOrg:     "mail.hermex.test",
		ReportContact: "mailto:postmaster@mail.hermex.test",
		ReportDomain:  "mail.hermex.test",
		DMARCLookup:   dmarcRecord(record),
	}
}

// queuedReport returns the one report the spool holds and the aggregate report
// its attachment carries, read back through the parser the MTA applies to
// received reports.
func queuedReport(t *testing.T, sp *Spool) (Item, []byte, *mailreport.Aggregate) {
	t.Helper()
	items, err := sp.Claim(time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queued %d message(s), want 1: %+v", len(items), items)
	}
	res, err := mailreport.Extract(items[0].Body)
	if err != nil {
		t.Fatalf("the report email does not read back as a report: %v\n%s", err, items[0].Body)
	}
	return items[0], items[0].Body, res.Aggregate
}

// TestDMARCReportCountsAndSends proves the daily pass sends one report whose rows
// carry the recorded counts, identifiers and raw results, and the policy the
// domain publishes.
func TestDMARCReportCountsAndSends(t *testing.T) {
	sp := openSpool(t)
	seedDMARC(t, sp)
	w := dmarcWorker(sp, "v=DMARC1; p=quarantine; aspf=s; rua=mailto:dmarc@example.test")
	if err := w.sendDMARCReports(context.Background(), reportDay); err != nil {
		t.Fatal(err)
	}
	it, raw, agg := queuedReport(t, sp)
	wantEq(t, it.Recipient, "dmarc@example.test", "recipient")
	wantEq(t, it.From, "noreply@mail.hermex.test", "envelope sender")
	for _, want := range []string{
		"Report Domain: example.test Submitter: mail.hermex.test Report-ID: <",
		"application/gzip",
		"mail.hermex.test!example.test!",
		".xml.gz",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("report email lacks %q", want)
		}
	}
	wantEq(t, agg.Domain, "example.test", "policy domain")
	wantEq(t, agg.P, "quarantine", "published p")
	wantEq(t, agg.ASPF, "s", "published aspf")
	wantEq(t, agg.ADKIM, "r", "adkim default")
	wantEq(t, agg.Pct, "100", "pct default")
	wantEq(t, agg.Email, "postmaster@mail.hermex.test", "report contact")
	if len(agg.Records) != 2 {
		t.Fatalf("records = %+v, want 2 rows", agg.Records)
	}
	wantEq(t, agg.Records[0].Count, int64(2), "identical messages share a row")
	wantEq(t, agg.Records[0].DKIMResults[0].Domain, "example.test", "DKIM result domain")
	wantEq(t, agg.Records[1].Disposition, "quarantine", "disposition")
	wantEq(t, agg.Records[1].EnvelopeFrom, "spoof.test", "envelope_from")
	wantEq(t, len(agg.Records[1].DKIMResults), 0, "no DKIM results")
}

// TestDMARCReportIsIdempotent proves a second pass over the same day sends
// nothing more.
func TestDMARCReportIsIdempotent(t *testing.T) {
	sp := openSpool(t)
	seedDMARC(t, sp)
	w := dmarcWorker(sp, "v=DMARC1; p=none; rua=mailto:dmarc@example.test")
	for range 2 {
		if err := w.sendDMARCReports(context.Background(), reportDay); err != nil {
			t.Fatal(err)
		}
	}
	queued, err := sp.List()
	if err != nil {
		t.Fatal(err)
	}
	wantEq(t, len(queued), 1, "reports after two passes")
}

// TestDMARCReportExternalDestination proves a destination outside the policy
// domain's organization gets the report only when it publishes the RFC 7489 §7.1
// authorization record, and that the record is looked up at the right name.
func TestDMARCReportExternalDestination(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer []string
		err    error
		want   int
	}{
		{"authorized", []string{"v=DMARC1"}, nil, 1},
		{"no record", nil, nil, 0},
		{"other record", []string{"v=spf1 -all"}, nil, 0},
		{"lookup error", nil, errors.New("SERVFAIL"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := openSpool(t)
			seedDMARC(t, sp)
			w := dmarcWorker(sp, "v=DMARC1; p=none; rua=mailto:reports@collector.test")
			var asked string
			w.DMARCLookupTXT = func(name string) ([]string, error) { asked = name; return tc.answer, tc.err }
			if err := w.sendDMARCReports(context.Background(), reportDay); err != nil {
				t.Fatal(err)
			}
			wantEq(t, asked, "example.test._report._dmarc.collector.test", "authorization record name")
			queued, err := sp.List()
			if err != nil {
				t.Fatal(err)
			}
			wantEq(t, len(queued), tc.want, "reports queued")
		})
	}
}

// TestDMARCReportSameOrganizationSkipsCheck proves a destination in the policy
// domain's own organization needs no authorization record.
func TestDMARCReportSameOrganizationSkipsCheck(t *testing.T) {
	sp := openSpool(t)
	seedDMARC(t, sp)
	w := dmarcWorker(sp, "v=DMARC1; p=none; rua=mailto:dmarc@reports.example.test")
	w.DMARCLookupTXT = func(name string) ([]string, error) {
		t.Errorf("authorization lookup %q for a destination in the same organization", name)
		return nil, nil
	}
	if err := w.sendDMARCReports(context.Background(), reportDay); err != nil {
		t.Fatal(err)
	}
	queued, err := sp.List()
	if err != nil {
		t.Fatal(err)
	}
	wantEq(t, len(queued), 1, "reports queued")
}

// TestDMARCReportSizeLimit proves a destination whose "!" limit the report exceeds
// gets nothing, while one with room gets the report.
func TestDMARCReportSizeLimit(t *testing.T) {
	sp := openSpool(t)
	seedDMARC(t, sp)
	w := dmarcWorker(sp, "v=DMARC1; p=none; rua=mailto:small@example.test!100,mailto:big@example.test!1m")
	if err := w.sendDMARCReports(context.Background(), reportDay); err != nil {
		t.Fatal(err)
	}
	it, _, _ := queuedReport(t, sp)
	wantEq(t, it.Recipient, "big@example.test", "the destination with room")
}

// TestDMARCReportDisabledSendsNothingButPrunes proves the operator setting stops
// sending while old counters are still pruned.
func TestDMARCReportDisabledSendsNothingButPrunes(t *testing.T) {
	sp := openSpool(t)
	seedDMARC(t, sp)
	old := reportDay.AddDate(0, 0, -30)
	if err := sp.RecordDMARC(old, DMARCObservation{PolicyDomain: "old.test", SourceIP: "192.0.2.1", DKIM: "fail", SPF: "fail", Disposition: "none"}); err != nil {
		t.Fatal(err)
	}
	w := dmarcWorker(sp, "v=DMARC1; p=none; rua=mailto:dmarc@example.test")
	w.DMARCEnabled = func() bool { return false }
	w.dailyDMARCReports(context.Background(), reportDay, reportDay.AddDate(0, 0, -7))
	queued, err := sp.List()
	if err != nil {
		t.Fatal(err)
	}
	wantEq(t, len(queued), 0, "reports sent while disabled")
	left, err := sp.UnreportedDMARCDomains(old)
	if err != nil {
		t.Fatal(err)
	}
	wantEq(t, len(left), 0, "old counters left after prune")
	left, err = sp.UnreportedDMARCDomains(reportDay)
	if err != nil {
		t.Fatal(err)
	}
	wantEq(t, len(left), 1, "recent counters kept")
}

// TestParseDMARCURI pins the rua= forms the pass accepts and refuses.
func TestParseDMARCURI(t *testing.T) {
	for _, tc := range []struct {
		uri   string
		addr  string
		limit int64
		ok    bool
	}{
		{"mailto:a@example.test", "a@example.test", 0, true},
		{"MAILTO:a@example.test!10k", "a@example.test", 10 << 10, true},
		{"mailto:a@example.test!2M", "a@example.test", 2 << 20, true},
		{"mailto:a@example.test!500", "a@example.test", 500, true},
		{"mailto:a%40b@example.test?subject=x", "a@b@example.test", 0, true},
		{"https://example.test/dmarc", "", 0, false},
		{"mailto:", "", 0, false},
		{"mailto:nobody", "", 0, false},
		{"mailto:a@example.test!k", "", 0, false},
		{"mailto:a@example.test!-1", "", 0, false},
		{"mailto:a@example.test!99999999999999999999", "", 0, false},
	} {
		got, err := parseDMARCURI(tc.uri)
		if (err == nil) != tc.ok {
			t.Errorf("%q: err = %v, want ok=%v", tc.uri, err, tc.ok)
			continue
		}
		if tc.ok && (got.addr != tc.addr || got.limit != tc.limit) {
			t.Errorf("%q = %+v, want %s limit %d", tc.uri, got, tc.addr, tc.limit)
		}
	}
}

// TestDMARCReportAttachmentIsGzipXML proves the attachment is the gzip-compressed
// XML document itself, the form RFC 7489 §7.2.1.1 prescribes.
func TestDMARCReportAttachmentIsGzipXML(t *testing.T) {
	sp := openSpool(t)
	seedDMARC(t, sp)
	w := dmarcWorker(sp, "v=DMARC1; p=none; rua=mailto:dmarc@example.test")
	if err := w.sendDMARCReports(context.Background(), reportDay); err != nil {
		t.Fatal(err)
	}
	_, raw, _ := queuedReport(t, sp)
	root := mime.ParseStructure(raw)
	if len(root.Children) != 2 {
		t.Fatalf("parts = %d, want a note and the report", len(root.Children))
	}
	att := root.Children[1]
	wantEq(t, att.Type+"/"+att.Subtype, "application/gzip", "attachment type")
	data, err := att.DecodedContent()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("attachment is not gzip: %v (%s)", err, base64.StdEncoding.EncodeToString(data[:min(len(data), 16)]))
	}
	doc, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(doc, []byte("<?xml")) {
		t.Errorf("attachment is not an XML document: %.60s", doc)
	}
}

// TestDMARCReportUnsignedIsLogged proves a pass that sends reports records once
// that they go out unsigned when the hostname has no DKIM key, records a failed
// key lookup, stays silent when a key exists, and still sends in every case.
func TestDMARCReportUnsignedIsLogged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		found bool
		err   error
		want  string
	}{
		{"no key", false, nil, "dmarc.report.unsigned"},
		{"lookup fails", false, errors.New("db down"), "dmarc.signing.check_failed"},
		{"key", true, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := openSpool(t)
			seedDMARC(t, sp)
			sink := &eventSink{}
			w := dmarcWorker(sp, "v=DMARC1; p=none; rua=mailto:dmarc@example.test")
			w.Logger = logging.New(sink)
			w.DMARCSignable = func() (bool, error) { return tc.found, tc.err }
			if err := w.sendDMARCReports(context.Background(), reportDay); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, e := range sink.events {
				names = append(names, e.Name)
			}
			if got := strings.Join(names, ","); got != tc.want {
				t.Errorf("events = %q, want %q", got, tc.want)
			}
			queued, err := sp.List()
			if err != nil {
				t.Fatal(err)
			}
			wantEq(t, len(queued), 1, "reports queued")
		})
	}
}

// eventSink collects the events a test's logger emits.
type eventSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (s *eventSink) Write(e logging.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}
