package health

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
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
	ln.Close()
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
// "ok" — so the monitor can tell a degraded daemon from a healthy one.
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
	defer resp.Body.Close()
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
		comps[0].Shutdown(context.Background())
		<-errc
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
	defer resp.Body.Close()
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
	old := buildinfo.Commit
	buildinfo.Commit = "abc1234-dirty"
	defer func() { buildinfo.Commit = old }()

	addr := freeAddr(t)
	st := statusFrom(t, addr, Components(addr, "imap"))
	if st.Version != "abc1234-dirty" {
		t.Errorf("version = %q, want the binary's own stamp", st.Version)
	}
	if st.Service != "imap" {
		t.Errorf("service = %q, want imap", st.Service)
	}
}

// TestComponentsReportAnUnstampedBuildHonestly proves a binary carrying no stamp
// says so rather than answering with an empty string, which reads as a value the
// operator is meant to compare against another daemon's.
func TestComponentsReportAnUnstampedBuildHonestly(t *testing.T) {
	old := buildinfo.Commit
	buildinfo.Commit = ""
	defer func() { buildinfo.Commit = old }()

	addr := freeAddr(t)
	if st := statusFrom(t, addr, Components(addr, "imap")); st.Version == "" {
		t.Error("an unstamped build reports an empty version, which reads as a value")
	}
}
