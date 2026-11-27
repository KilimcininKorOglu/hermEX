package mta

import (
	"bytes"
	"compress/gzip"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// tlsReportJSON is the JSON document of a TLS report for domain.
func tlsReportJSON(domain string) string {
	body := tlsReportMail(domain)
	return body[strings.Index(body, "{"):]
}

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write([]byte(s))
	mustNoErr(t, "gzip the report", err)
	mustNoErr(t, "close the gzip stream", zw.Close())
	return buf.Bytes()
}

// postReport posts body with contentType to h, as the gateway forwards a client,
// and returns the status and response recorder.
func postReport(h *TLSReportHandler, method, contentType string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/tlsrpt", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Forwarded-For", "203.0.113.40")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

type tlsReportRig struct {
	h    *TLSReportHandler
	rec  *fakeReports
	sink *captureSink
}

func newTLSReportRig() tlsReportRig {
	rec := &fakeReports{domains: map[string]int64{"hermex.test": 7}}
	sink := &captureSink{}
	return tlsReportRig{h: NewTLSReportHandler(rec, logging.New(sink)), rec: rec, sink: sink}
}

// TestAPostedTLSReportIsStored is the feature: both media types RFC 8460 §3 names
// are accepted, and the report is stored under its domain as an HTTPS arrival,
// with the client the gateway forwarded rather than the gateway itself.
func TestAPostedTLSReportIsStored(t *testing.T) {
	for name, post := range map[string]struct {
		ctype string
		body  []byte
	}{
		"json": {"application/tlsrpt+json", []byte(tlsReportJSON("hermex.test"))},
		"gzip": {"application/tlsrpt+gzip; charset=binary", gzipped(t, tlsReportJSON("hermex.test"))},
	} {
		rig := newTLSReportRig()
		rr := postReport(rig.h, http.MethodPost, post.ctype, post.body)
		wantEq(t, name+": status", rr.Code, http.StatusOK)
		if len(rig.rec.stored) != 1 {
			t.Fatalf("%s: stored reports = %d, want 1", name, len(rig.rec.stored))
		}
		src := rig.rec.stored[0]
		wantEq(t, name+": domain id", src.DomainID, int64(7))
		wantEq(t, name+": transport", src.Via, directory.ViaHTTPS)
		wantEq(t, name+": client", src.RemoteAddr, "203.0.113.40")
		wantEq(t, name+": mail sender", src.MailFrom, "")
		if _, ok := findEvent(rig.sink.events, "report.stored"); !ok {
			t.Errorf("%s: no report.stored event", name)
		}
	}
}

// TestAPostedReportIsRefusedWithoutBeingStored covers every refusal: the wrong
// method or media type, a body past the cap, a body that is no report, and a
// report for a domain this server does not host. None of them stores anything.
func TestAPostedReportIsRefusedWithoutBeingStored(t *testing.T) {
	report := []byte(tlsReportJSON("hermex.test"))
	multiReport := []byte(`{"organization-name":"Reporter","date-range":{"start-datetime":"2025-09-19T00:00:00Z","end-datetime":"2025-09-19T23:59:59Z"},"contact-info":"tls@reporter.example","report-id":"r-m","policies":[{"policy":{"policy-type":"sts","policy-domain":"hermex.test"},"summary":{"total-successful-session-count":1,"total-failure-session-count":0}},{"policy":{"policy-type":"sts","policy-domain":"victim.test"},"summary":{"total-successful-session-count":1,"total-failure-session-count":0}}]}`)
	cases := []struct {
		name, method, ctype string
		body                []byte
		maxBody             int64
		want                int
		event               string
	}{
		{"GET", http.MethodGet, "application/tlsrpt+json", nil, 0, http.StatusMethodNotAllowed, ""},
		{"plain JSON type", http.MethodPost, "application/json", report, 0, http.StatusUnsupportedMediaType, ""},
		{"over the cap", http.MethodPost, "application/tlsrpt+json", report, 16, http.StatusRequestEntityTooLarge, "report.too_large"},
		{"not a report", http.MethodPost, "application/tlsrpt+json", []byte("{}"), 0, http.StatusBadRequest, "report.parse_failed"},
		{"bad gzip", http.MethodPost, "application/tlsrpt+gzip", report, 0, http.StatusBadRequest, "report.parse_failed"},
		{"foreign domain", http.MethodPost, "application/tlsrpt+json", []byte(tlsReportJSON("victim.test")), 0, http.StatusForbidden, "report.domain_mismatch"},
		{"multi-domain report", http.MethodPost, "application/tlsrpt+json", multiReport, 0, http.StatusForbidden, "report.domain_mismatch"},
	}
	for _, c := range cases {
		rig := newTLSReportRig()
		rig.h.SetMaxBodyBytes(c.maxBody)
		// A negative cap is ignored: applied, it would refuse every body as too
		// large and turn each case below into a 413.
		rig.h.SetMaxBodyBytes(-1)
		rr := postReport(rig.h, c.method, c.ctype, c.body)
		wantEq(t, c.name+": status", rr.Code, c.want)
		if c.method == http.MethodGet {
			wantEq(t, "Allow header on 405", rr.Header().Get("Allow"), http.MethodPost)
		}
		wantEq(t, c.name+": stored reports", len(rig.rec.stored), 0)
		if c.event == "" {
			continue
		}
		if _, ok := findEvent(rig.sink.events, c.event); !ok {
			t.Errorf("%s: no %s event", c.name, c.event)
		}
	}
}

// TestAResentPostedReportIsAccepted: RFC 8460 has the reporter retry a report it
// is not sure arrived, and a copy already stored must still read as delivered, or
// the reporter keeps retrying.
func TestAResentPostedReportIsAccepted(t *testing.T) {
	rig := newTLSReportRig()
	rig.rec.err = directory.ErrDuplicateReport
	rr := postReport(rig.h, http.MethodPost, "application/tlsrpt+json", []byte(tlsReportJSON("hermex.test")))
	wantEq(t, "status", rr.Code, http.StatusOK)
	if _, ok := findEvent(rig.sink.events, "report.duplicate"); !ok {
		t.Error("no report.duplicate event")
	}
}

// TestAPostedReportStoreFailureIsA500WithoutDetail: the cause of a failed store
// is logged, and the answer to an unauthenticated client names none of it.
func TestAPostedReportStoreFailureIsA500WithoutDetail(t *testing.T) {
	rig := newTLSReportRig()
	rig.rec.err = errors.New("dial tcp 10.0.0.5:3306: connection refused")
	req := httptest.NewRequest(http.MethodPost, "/tlsrpt", strings.NewReader(tlsReportJSON("hermex.test")))
	req.Header.Set("Content-Type", "application/tlsrpt+json")
	rr := httptest.NewRecorder()
	rig.h.ServeHTTP(rr, req)

	wantEq(t, "status", rr.Code, http.StatusInternalServerError)
	if strings.Contains(rr.Body.String(), "10.0.0.5") {
		t.Errorf("the answer leaks the cause: %q", rr.Body.String())
	}
	e, ok := findEvent(rig.sink.events, "report.store_failed")
	if !ok {
		t.Fatal("no report.store_failed event")
	}
	wantEq(t, "the logged cause", e.Err, "dial tcp 10.0.0.5:3306: connection refused")
}
