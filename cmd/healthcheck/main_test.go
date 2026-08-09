package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestProbeAcceptsAHealthyDaemon proves the ready case passes, which is what
// keeps a container marked healthy.
func TestProbeAcceptsAHealthyDaemon(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("probed %q, want /healthz", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"service":"imap","ok":true}`))
	}))
	defer ts.Close()

	if err := probe(ts.URL + "/healthz"); err != nil {
		t.Errorf("a 200 was reported as unhealthy: %v", err)
	}
}

// TestProbeRejectsADegradedDaemon is the case the whole change exists for: the
// process is alive and answering, and says it is not ready. Without this the
// container looks identical to a healthy one.
func TestProbeRejectsADegradedDaemon(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false,"checks":{"directory":"dial tcp: connection refused"}}`))
	}))
	defer ts.Close()

	if err := probe(ts.URL + "/healthz"); err == nil {
		t.Error("a 503 readiness answer was reported as healthy")
	}
}

// TestProbeRejectsAnUnreachableDaemon covers the listener being gone entirely.
func TestProbeRejectsAnUnreachableDaemon(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL + "/healthz"
	ts.Close() // nothing is listening now

	if err := probe(url); err == nil {
		t.Error("an unreachable endpoint was reported as healthy")
	}
}

// TestProbeGivesUp proves the attempt is bounded. A daemon wedged mid-response
// must fail its healthcheck rather than hold the probe open forever, since
// Docker's own timeout would then be the only thing stopping it.
func TestProbeGivesUp(t *testing.T) {
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // never answers
	}))
	defer func() { close(block); ts.Close() }()

	start := time.Now()
	if err := probe(ts.URL + "/healthz"); err == nil {
		t.Fatal("a daemon that never answered was reported as healthy")
	}
	if elapsed := time.Since(start); elapsed > probeTimeout+2*time.Second {
		t.Errorf("the probe took %s, want it bounded near %s", elapsed, probeTimeout)
	}
}
