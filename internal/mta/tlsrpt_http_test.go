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
// and returns the status.
func postReport(h *TLSReportHandler, method, contentType string, body []byte) int {
	req := httptest.NewRequest(method, "/tlsrpt", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Forwarded-For", "203.0.113.40")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code
}

func newTLSReportRig() (*TLSReportHandler, *fakeReports, *captureSink) {
	rec := &fakeReports{domains: map[string]int64{"hermex.test": 7}}
	sink := &captureSink{}
	return NewTLSReportHandler(rec, logging.New(sink)), rec, sink
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
		h, rec, _ := newTLSReportRig()
		wantEq(t, name+": status", postReport(h, http.MethodPost, post.ctype, post.body), http.StatusOK)
		if len(rec.stored) != 1 {
			t.Fatalf("%s: stored reports = %d, want 1", name, len(rec.stored))
		}
		src := rec.stored[0]
		wantEq(t, name+": domain id", src.DomainID, int64(7))
		wantEq(t, name+": transport", src.Via, directory.ViaHTTPS)
		wantEq(t, name+": client", src.RemoteAddr, "203.0.113.40")
		wantEq(t, name+": mail sender", src.MailFrom, "")
	}
}

// TestAPostedReportIsRefusedWithoutBeingStored covers every refusal: the wrong
// method or media type, a body past the cap, a body that is no report, and a
// report for a domain this server does not host. None of them stores anything.
func TestAPostedReportIsRefusedWithoutBeingStored(t *testing.T) {
	report := []byte(tlsReportJSON("hermex.test"))
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
	}
	for _, c := range cases {
		h, rec, sink := newTLSReportRig()
		h.SetMaxBodyBytes(c.maxBody)
		wantEq(t, c.name+": status", postReport(h, c.method, c.ctype, c.body), c.want)
		wantEq(t, c.name+": stored reports", len(rec.stored), 0)
		if c.event == "" {
			continue
		}
		if _, ok := findEvent(sink.events, c.event); !ok {
			t.Errorf("%s: no %s event", c.name, c.event)
		}
	}
}

// TestAResentPostedReportIsAccepted: RFC 8460 has the reporter retry a report it
// is not sure arrived, and a copy already stored must still read as delivered, or
// the reporter keeps retrying.
func TestAResentPostedReportIsAccepted(t *testing.T) {
	h, rec, sink := newTLSReportRig()
	rec.err = directory.ErrDuplicateReport
	wantEq(t, "status", postReport(h, http.MethodPost, "application/tlsrpt+json", []byte(tlsReportJSON("hermex.test"))), http.StatusOK)
	if _, ok := findEvent(sink.events, "report.duplicate"); !ok {
		t.Error("no report.duplicate event")
	}
}

// TestAPostedReportStoreFailureIsA500WithoutDetail: the cause of a failed store
// is logged, and the answer to an unauthenticated client names none of it.
func TestAPostedReportStoreFailureIsA500WithoutDetail(t *testing.T) {
	h, rec, sink := newTLSReportRig()
	rec.err = errors.New("dial tcp 10.0.0.5:3306: connection refused")
	req := httptest.NewRequest(http.MethodPost, "/tlsrpt", strings.NewReader(tlsReportJSON("hermex.test")))
	req.Header.Set("Content-Type", "application/tlsrpt+json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	wantEq(t, "status", rr.Code, http.StatusInternalServerError)
	if strings.Contains(rr.Body.String(), "10.0.0.5") {
		t.Errorf("the answer leaks the cause: %q", rr.Body.String())
	}
	e, ok := findEvent(sink.events, "report.store_failed")
	if !ok {
		t.Fatal("no report.store_failed event")
	}
	wantEq(t, "the logged cause", e.Err, "dial tcp 10.0.0.5:3306: connection refused")
}
