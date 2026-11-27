package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// refusingWriter is a stream that takes nothing, the state of a closed stderr.
type refusingWriter struct{ err error }

func (w refusingWriter) Write([]byte) (int, error) { return 0, w.err }

// TestReportFallsBackWhenStderrRefuses: the probe failure is the only explanation
// of an unhealthy container, so a stderr that refuses it must not swallow it. A
// working stderr takes the report alone; a refusing one sends the failure and
// the refused write to stdout; when both refuse, report says so.
func TestReportFallsBackWhenStderrRefuses(t *testing.T) {
	probeErr := errors.New("http://127.0.0.1:8090/healthz reported 503 Service Unavailable")
	stderrErr := errors.New("stderr closed")

	var stderr, stdout bytes.Buffer
	if err := report(probeErr, &stderr, &stdout); err != nil {
		t.Fatalf("report with a working stderr: %v", err)
	}
	if !strings.Contains(stderr.String(), "503") || stdout.Len() != 0 {
		t.Errorf("stderr %q, stdout %q; want the report on stderr alone", stderr.String(), stdout.String())
	}

	stdout.Reset()
	if err := report(probeErr, refusingWriter{stderrErr}, &stdout); err != nil {
		t.Fatalf("report with a refusing stderr: %v", err)
	}
	if out := stdout.String(); !strings.Contains(out, "503") || !strings.Contains(out, "stderr closed") {
		t.Errorf("stdout %q; want the probe failure and the refused stderr write", out)
	}

	stdoutErr := errors.New("stdout closed")
	if err := report(probeErr, refusingWriter{stderrErr}, refusingWriter{stdoutErr}); !errors.Is(err, stdoutErr) {
		t.Errorf("report with both streams refusing = %v, want the stdout error", err)
	}
}

// TestProbeAcceptsAHealthyDaemon proves the ready case passes, which is what
// keeps a container marked healthy.
func TestProbeAcceptsAHealthyDaemon(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("probed %q, want /healthz", r.URL.Path)
		}
		if _, err := w.Write([]byte(`{"service":"imap","ok":true}`)); err != nil {
			t.Errorf("write the health answer: %v", err)
		}
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
		if _, err := w.Write([]byte(`{"ok":false,"checks":{"directory":"dial tcp: connection refused"}}`)); err != nil {
			t.Errorf("write the health answer: %v", err)
		}
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
