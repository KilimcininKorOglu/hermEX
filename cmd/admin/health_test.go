package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/health"
)

// stubPinger stands in for the directory connection.
type stubPinger struct{ err error }

func (p stubPinger) PingContext(context.Context) error { return p.err }

// TestAdminReportsItsDirectoryConnection is the defect. Every other daemon serves
// the same liveness contract, and the panel is the consumer of all of theirs for its
// Live monitor, yet it had none of its own: nothing outside it could tell a running
// panel from one whose directory connection is failing, since that surfaced only as
// individual request errors.
func TestAdminReportsItsDirectoryConnection(t *testing.T) {
	checks := adminHealthChecks(stubPinger{err: errors.New("dial tcp: connection refused")}, nil)
	if len(checks) != 1 || checks[0].Name != "directory" {
		t.Fatalf("checks = %+v, want one directory probe", checks)
	}

	ts := httptest.NewServer(health.Handler("admin", "", time.Now(), checks...))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var st health.Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.OK {
		t.Error("a failing directory reports healthy")
	}
	if st.Checks["directory"] == "ok" || st.Checks["directory"] == "" {
		t.Errorf("the directory failure is not reported: %v", st.Checks)
	}
	if st.Service != "admin" {
		t.Errorf("service = %q, want admin", st.Service)
	}
}

// TestAdminReportsHealthyWhenTheDirectoryAnswers guards the other direction: a
// probe that always reports a problem is as useless as none at all.
func TestAdminReportsHealthyWhenTheDirectoryAnswers(t *testing.T) {
	ts := httptest.NewServer(health.Handler("admin", "", time.Now(),
		adminHealthChecks(stubPinger{}, nil)...))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var st health.Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if !st.OK || st.Checks["directory"] != "ok" {
		t.Errorf("a working directory is reported as %v / %v", st.OK, st.Checks)
	}
}

// TestAdminHealthOmitsTheCertificateWithoutTLS proves the certificate probe is tied
// to actually serving TLS. Reporting the expiry of a certificate that is not in use
// would degrade the panel over something that cannot affect it.
func TestAdminHealthOmitsTheCertificateWithoutTLS(t *testing.T) {
	if checks := adminHealthChecks(stubPinger{}, nil); len(checks) != 1 {
		t.Errorf("checks without TLS = %d, want only the directory probe", len(checks))
	}
}
