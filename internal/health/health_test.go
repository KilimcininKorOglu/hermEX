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
