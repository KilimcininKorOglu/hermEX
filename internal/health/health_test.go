package health

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hermex/internal/buildinfo"
	"hermex/internal/lifecycle"
)

// freeAddr reserves a loopback port and releases it, returning the address for a
// component to bind. The brief gap is acceptable in a test.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}
	return addr
}

// TestHandlerHealthy proves a daemon with no failing checks reports 200 and a
// well-formed status carrying its identity and a non-negative uptime.
func TestHandlerHealthy(t *testing.T) {
	h := Handler("imap", "test", time.Now().Add(-5*time.Second))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: the status is a live reading", cc)
	}
	var st Status
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Service != "imap" || !st.OK || st.Uptime < 0 {
		t.Fatalf("status = %+v, want service imap, ok true, uptime >= 0", st)
	}
}

// TestHandlerDegraded proves a failing readiness check flips the daemon to 503
// with ok=false and the failing dependency named, while a passing check reads
// "ok", so the monitor can tell a degraded daemon from a healthy one.
func TestHandlerDegraded(t *testing.T) {
	h := Handler("mta", "test", time.Now(),
		Check{Name: "directory", Probe: func(context.Context) error { return errors.New("dial tcp: refused") }},
		Check{Name: "spool", Probe: func(context.Context) error { return nil }},
	)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var st Status
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.OK {
		t.Errorf("ok = true, want false when a check fails")
	}
	if st.Checks["directory"] != "dial tcp: refused" {
		t.Errorf("checks[directory] = %q, want the probe error", st.Checks["directory"])
	}
	if st.Checks["spool"] != "ok" {
		t.Errorf("checks[spool] = %q, want ok", st.Checks["spool"])
	}
}

// TestComponents proves the daemon-main helper is opt-in: an empty address
// disables health (nil, so nothing is added to the daemon), a set address yields
// exactly one component.
func TestComponents(t *testing.T) {
	if c := Components("", "imap"); c != nil {
		t.Errorf("Components(\"\") = %v, want nil (disabled)", c)
	}
	if c := Components("127.0.0.1:0", "imap"); len(c) != 1 {
		t.Errorf("Components(addr) = %d components, want 1", len(c))
	}
}

// TestComponentServesAndStops proves the component actually serves /healthz on a
// listener and then shuts down cleanly.
func TestComponentServesAndStops(t *testing.T) {
	addr := freeAddr(t)
	c := Component(addr, Handler("svc", "test", time.Now()))

	errc := make(chan error, 1)
	go func() { errc <- c.Start() }()
	defer func() {
		if err := c.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		if err := <-errc; err != nil && err != http.ErrServerClosed {
			t.Errorf("start returned %v, want ErrServerClosed", err)
		}
	}()

	// Poll until the listener is up, then hit /healthz.
	url := "http://" + addr + "/healthz"
	var resp *http.Response
	var err error
	for range 50 {
		resp, err = http.Get(url)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close response body: %v", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// statusFrom starts a component, reads its /healthz once, and shuts it down.
func statusFrom(t *testing.T, addr string, comps []lifecycle.Component) Status {
	t.Helper()
	if len(comps) != 1 {
		t.Fatalf("Components returned %d components, want 1", len(comps))
	}
	errc := make(chan error, 1)
	go func() { errc <- comps[0].Start() }()
	t.Cleanup(func() {
		if err := comps[0].Shutdown(context.Background()); err != nil {
			t.Errorf("health server Shutdown: %v", err)
		}
		if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("health server Start returned %v, want http.ErrServerClosed", err)
		}
	})
	var resp *http.Response
	var err error
	for range 50 {
		if resp, err = http.Get("http://" + addr + "/healthz"); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close response body: %v", err)
		}
	}()
	var st Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestComponentsReportTheBuildVersion is the defect. Every daemon builds its health
// listener through Components, which passed a literal empty string as the version,
// so every /healthz answered with an empty one and the admin Live monitor's Version
// column was permanently blank. That column is the one built-in answer to which
// daemons are still on an older binary after a rolling restart, which is the
// question schema compatibility turns on when they share one database.
func TestComponentsReportTheBuildVersion(t *testing.T) {
	oldCommit, oldSemVer := buildinfo.Commit, buildinfo.SemVer
	buildinfo.Commit, buildinfo.SemVer = "abc1234-dirty", "0.1.0"
	defer func() { buildinfo.Commit, buildinfo.SemVer = oldCommit, oldSemVer }()

	addr := freeAddr(t)
	st := statusFrom(t, addr, Components(addr, "imap"))
	if st.Version != "0.1.0-dev+abc1234-dirty" {
		t.Errorf("version = %q, want the binary's own release and stamp", st.Version)
	}
	if st.Service != "imap" {
		t.Errorf("service = %q, want imap", st.Service)
	}
}

// TestComponentsReportAnUnstampedBuildHonestly proves a binary carrying no stamp
// says so rather than answering with an empty string, which reads as a value the
// operator is meant to compare against another daemon's.
func TestComponentsReportAnUnstampedBuildHonestly(t *testing.T) {
	oldCommit, oldSemVer := buildinfo.Commit, buildinfo.SemVer
	buildinfo.Commit, buildinfo.SemVer = "", ""
	defer func() { buildinfo.Commit, buildinfo.SemVer = oldCommit, oldSemVer }()

	addr := freeAddr(t)
	if st := statusFrom(t, addr, Components(addr, "imap")); st.Version == "" {
		t.Error("an unstamped build reports an empty version, which reads as a value")
	}
}

// TestHandlerRejectsHostileRequests proves that disallowed HTTP methods and
// unknown or oversized paths are refused (RFC 9110 405 / 404) without invoking
// any readiness probe, leaving handler state unchanged.
func TestHandlerRejectsHostileRequests(t *testing.T) {
	var probeCalls atomic.Int32
	h := Handler("imap", "test", time.Now(), Check{
		Name: "directory",
		Probe: func(context.Context) error {
			probeCalls.Add(1)
			return nil
		},
	})

	cases := []struct {
		name       string
		method     string
		target     string
		wantStatus int
	}{
		{name: "POST method", method: http.MethodPost, target: "/healthz", wantStatus: http.StatusMethodNotAllowed},
		{name: "PUT method", method: http.MethodPut, target: "/healthz", wantStatus: http.StatusMethodNotAllowed},
		{name: "DELETE method", method: http.MethodDelete, target: "/healthz", wantStatus: http.StatusMethodNotAllowed},
		{name: "root path", method: http.MethodGet, target: "/", wantStatus: http.StatusNotFound},
		{name: "forged subpath", method: http.MethodGet, target: "/healthz/extra", wantStatus: http.StatusNotFound},
		{name: "unrelated path", method: http.MethodGet, target: "/admin", wantStatus: http.StatusNotFound},
		{name: "oversized path", method: http.MethodGet, target: "/" + strings.Repeat("a", 4096), wantStatus: http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("%s %s: status = %d, want %d", tc.method, tc.target, rec.Code, tc.wantStatus)
			}
			if got := probeCalls.Load(); got != 0 {
				t.Fatalf("probe invoked %d times on refused request, want 0", got)
			}
		})
	}

	// Verify a legitimate GET /healthz still succeeds after the refused requests.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz after refusals: status = %d, want 200", rec.Code)
	}
	if got := probeCalls.Load(); got != 1 {
		t.Fatalf("probe invoked %d times after valid GET, want 1", got)
	}
}
