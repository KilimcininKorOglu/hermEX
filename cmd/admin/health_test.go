package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/health"
	"hermex/internal/tlscert"
	"hermex/internal/tlstest"
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

// certStore serves one stored certificate to a tlscert.Provider.
type certStore struct{ cert directory.TLSCertData }

func (s certStore) LoadTLSCerts() ([]directory.TLSCertData, error) {
	return []directory.TLSCertData{s.cert}, nil
}
func (s certStore) TLSCertVersion() (int64, int64, error) { return 1, 1, nil }

// TestAdminHealthReportsTheCertificateWithTLS is the other half of the TLS
// contract every daemon's /healthz shares: once the panel serves a certificate,
// its remaining validity is a reported check beside the directory probe, so an
// expiring certificate shows on the Live monitor before clients start failing.
func TestAdminHealthReportsTheCertificateWithTLS(t *testing.T) {
	certPath, keyPath, err := tlstest.SelfSigned(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := tlscert.New(nil, certStore{directory.TLSCertData{CertPEM: string(certPEM), KeyPEM: string(keyPEM)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !provider.TLSEnabled() {
		t.Fatal("the provider holds a certificate but reports TLS off")
	}

	checks := adminHealthChecks(stubPinger{}, provider)
	if len(checks) != 2 || checks[0].Name != "directory" || checks[1].Name != tlscert.ExpiryCheck(provider).Name {
		t.Errorf("checks with TLS = %+v, want the directory probe and the certificate check", checks)
	}
}
