package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/health"
	"hermex/internal/tlscert"
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

// certExpiringIn returns a self-signed certificate and key that stop being valid
// after d.
func certExpiringIn(t *testing.T, d time.Duration) directory.TLSCertData {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "admin.hermex.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(d),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return directory.TLSCertData{
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})),
	}
}

// TestAdminHealthReportsTheCertificateWithTLS is the other half of the TLS
// contract every daemon's /healthz shares: once the panel serves a certificate,
// its remaining validity is reported beside the directory probe, so a
// certificate three days from expiry turns the panel's /healthz unhealthy and
// shows on the Live monitor before clients start failing.
func TestAdminHealthReportsTheCertificateWithTLS(t *testing.T) {
	provider, err := tlscert.New(nil, certStore{certExpiringIn(t, 72*time.Hour)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(health.Handler("admin", "", time.Now(), adminHealthChecks(stubPinger{}, provider)...))
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
	if st.OK || st.Checks["directory"] != "ok" {
		t.Errorf("ok = %v checks = %v, want unhealthy on the certificate alone", st.OK, st.Checks)
	}
	reported := false
	for name, msg := range st.Checks {
		reported = reported || (name != "directory" && strings.Contains(msg, "expires"))
	}
	if !reported {
		t.Errorf("checks = %v, want a check reporting the coming expiry", st.Checks)
	}
}
