package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// unreachableGateway builds a front door whose only backend does not exist, the
// shape of a daemon that is down.
func unreachableGateway(t *testing.T) http.Handler {
	t.Helper()
	// Port 1 on loopback refuses connections immediately, so the proxy's error
	// path runs without waiting on a dial timeout.
	h, err := Handler([]Route{{Prefix: "/", Target: "http://127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestBrowserSeesAPageWhenTheBackendIsDown proves a user whose webmail is down
// gets something to read. The standard proxy answers an unreachable backend with
// a bare status and an empty body, so the browser showed a blank page and the
// user had nothing to report.
func TestBrowserSeesAPageWhenTheBackendIsDown(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	rec := httptest.NewRecorder()
	unreachableGateway(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (the service is temporarily unreachable)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want html for a browser", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<html") || !strings.Contains(body, "try again") && !strings.Contains(body, "Try again") {
		t.Errorf("the body is not a usable page: %q", body)
	}
	// The page must not hand a stranger the internal topology.
	if strings.Contains(body, "127.0.0.1") || strings.Contains(body, "connection refused") {
		t.Error("the error page names the internal backend or the transport error")
	}
}

// TestProtocolClientGetsNoHTML is the wire-compatibility guard. The same front
// door carries Outlook, ActiveSync devices and DAV clients; handing one of those
// an HTML body where it expects a protocol response turns a clear transport
// failure into a parse failure.
func TestProtocolClientGetsNoHTML(t *testing.T) {
	for _, accept := range []string{"", "text/xml", "application/vnd.ms-sync.wbxml", "*/*"} {
		t.Run(accept, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync", nil)
			if accept != "" {
				req.Header.Set("Accept", accept)
			}
			rec := httptest.NewRecorder()
			unreachableGateway(t).ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "<html") {
				t.Error("a protocol client was handed an HTML body")
			}
		})
	}
}

// TestUnroutedPathAnswersInKind proves the no-such-route case follows the same
// rule: a page for a browser, the bare status for anything else.
func TestUnroutedPathAnswersInKind(t *testing.T) {
	h, err := Handler([]Route{{Prefix: "/ews", Target: "http://127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}

	browser := httptest.NewRequest(http.MethodGet, "/nowhere", nil)
	browser.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, browser)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Error("a browser got no page for an unrouted path")
	}

	client := httptest.NewRequest(http.MethodGet, "/nowhere", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, client)
	if strings.Contains(rec.Body.String(), "<html") {
		t.Error("a non-browser got an HTML body for an unrouted path")
	}
}

// TestErrorPageShowsTheRequestID proves the page carries the correlation id the
// access log recorded, so a user quoting it leads support straight to the line
// that explains the failure.
func TestErrorPageShowsTheRequestID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	// The logging middleware sets this before the handler runs; stand in for it.
	rec.Header().Set("X-Request-Id", "abc123")
	unreachableGateway(t).ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "abc123") {
		t.Error("the page shows no reference id; a user reporting the outage has nothing to quote")
	}
}

// TestErrorPageEscapesTheRequestID feeds the page a request id carrying markup.
// The access-log middleware echoes an inbound X-Request-Id, so the value is the
// client's own: written raw it would run script in the site's origin (reflected
// XSS, OWASP A03). It must reach the page as text.
func TestErrorPageEscapesTheRequestID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-Id", `"><script>alert(1)</script>`)
	unreachableGateway(t).ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "<script>") {
		t.Fatalf("the request id reached the page as markup: %q", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("the request id was dropped rather than shown as text: %q", body)
	}
}

// TestHostileAcceptAndUnroutedPathDoNotReachBackend proves forged or oversized
// Accept headers are not treated as text/html and unrouted paths are refused
// with 502 without forwarding to a live backend.
func TestHostileAcceptAndUnroutedPathDoNotReachBackend(t *testing.T) {
	var hits atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if _, err := io.WriteString(w, "ok"); err != nil {
			t.Errorf("write backend response: %v", err)
		}
	}))
	defer backend.Close()

	h, err := Handler([]Route{{Prefix: "/ews/", Target: backend.URL}})
	if err != nil {
		t.Fatal(err)
	}

	oversizedAccept := strings.Repeat("application/octet-stream,", 200)
	cases := []struct {
		name   string
		path   string
		accept string
	}{
		{"forged-media-suffix", "/nowhere", "text/htmlx"},
		{"forged-media-markup", "/ews-admin", "text/html<script>"},
		{"missing-accept", "/ews", ""},
		{"oversized-accept", "/" + strings.Repeat("a", 4096), oversizedAccept},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadGateway {
				t.Errorf("status = %d, want 502", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "<html") {
				t.Errorf("hostile Accept %q received HTML body", tc.accept)
			}
			if got := hits.Load(); got != 0 {
				t.Errorf("backend hits = %d, want 0 on unrouted request", got)
			}
		})
	}

	// Follow-up valid request confirms the backend is reachable when routed.
	validReq := httptest.NewRequest(http.MethodGet, "/ews/Exchange.asmx", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, validReq)
	if rec.Code != http.StatusOK || hits.Load() != 1 {
		t.Errorf("valid request: status = %d, hits = %d, want 200 and 1", rec.Code, hits.Load())
	}
}
