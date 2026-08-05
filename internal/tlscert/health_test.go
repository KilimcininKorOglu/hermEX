package tlscert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/tlstest"
)

// genCertExpiring returns a self-signed certificate that stops being valid at
// notAfter, so the expiry paths run against real material.
func genCertExpiring(t *testing.T, cn string, notAfter time.Time) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

// providerWith builds a provider over a store holding one certificate per entry.
func providerWith(t *testing.T, certs map[string]time.Time) *Provider {
	t.Helper()
	store := &fakeStore{version: 1, count: int64(len(certs))}
	for name, notAfter := range certs {
		cert, key := genCertExpiring(t, name, notAfter)
		store.certs = append(store.certs, directory.TLSCertData{Name: name, CertPEM: cert, KeyPEM: key})
	}
	p, err := New(nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestExpiryReportsTheSoonestCertificate proves Expiry answers with the earliest
// NotAfter across the served set: a handshake may land on any of them, so the one
// that lapses first is the one an operator has to renew.
func TestExpiryReportsTheSoonestCertificate(t *testing.T) {
	soon := time.Now().Add(20 * 24 * time.Hour).Truncate(time.Second)
	p := providerWith(t, map[string]time.Time{
		"a.example.com": time.Now().Add(300 * 24 * time.Hour),
		"b.example.com": soon,
		"c.example.com": time.Now().Add(90 * 24 * time.Hour),
	})
	got, ok := p.Expiry()
	if !ok {
		t.Fatal("Expiry reported no certificate, want the stored ones")
	}
	if !got.Equal(soon.UTC()) && got.Unix() != soon.Unix() {
		t.Errorf("Expiry = %s, want the soonest (%s)", got, soon)
	}
}

// TestExpiryReportsNothingWithoutCertificates proves a provider serving no
// certificate reports none, so a plaintext daemon is never judged on a certificate
// it does not have.
func TestExpiryReportsNothingWithoutCertificates(t *testing.T) {
	p, err := New(nil, &fakeStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Expiry(); ok {
		t.Error("Expiry reported a certificate on an empty provider")
	}
}

// TestExpiryCheckHealthyWhileFarFromExpiry proves a certificate with months left
// keeps the daemon healthy.
func TestExpiryCheckHealthyWhileFarFromExpiry(t *testing.T) {
	p := providerWith(t, map[string]time.Time{"a.example.com": time.Now().Add(60 * 24 * time.Hour)})
	if err := ExpiryCheck(p).Probe(context.Background()); err != nil {
		t.Errorf("probe = %v, want healthy 60 days out", err)
	}
}

// TestExpiryCheckWarnsInsideTheWindow proves a certificate that has not been renewed
// marks the daemon degraded before it lapses, and names the date so an operator can
// act.
func TestExpiryCheckWarnsInsideTheWindow(t *testing.T) {
	notAfter := time.Now().Add(7 * 24 * time.Hour)
	p := providerWith(t, map[string]time.Time{"a.example.com": notAfter})
	err := ExpiryCheck(p).Probe(context.Background())
	if err == nil {
		t.Fatal("probe healthy with 7 days left, want degraded inside the warning window")
	}
	if !strings.Contains(err.Error(), notAfter.UTC().Format("2006-01-02")) {
		t.Errorf("probe = %q, want the expiry date named", err)
	}
	if !strings.Contains(err.Error(), "expires on") {
		t.Errorf("probe = %q, want it reported as expiring, not expired", err)
	}
}

// TestExpiryCheckFailsOnceExpired proves a lapsed certificate is reported as
// expired, the state in which clients can no longer complete a handshake.
func TestExpiryCheckFailsOnceExpired(t *testing.T) {
	p := providerWith(t, map[string]time.Time{"a.example.com": time.Now().Add(-time.Hour)})
	err := ExpiryCheck(p).Probe(context.Background())
	if err == nil {
		t.Fatal("probe healthy with an expired certificate")
	}
	if !strings.Contains(err.Error(), "expired on") {
		t.Errorf("probe = %q, want it reported as expired", err)
	}
}

// TestExpiryCheckSilentWithoutCertificates proves the probe passes when there is no
// certificate at all, so a provider without TLS never fails a daemon's readiness.
func TestExpiryCheckSilentWithoutCertificates(t *testing.T) {
	p, err := New(nil, &fakeStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ExpiryCheck(p).Probe(context.Background()); err != nil {
		t.Errorf("probe = %v, want no complaint when nothing is served over TLS", err)
	}
}

// TestExpiryCheckFollowsAReload proves the check reads the live snapshot rather than
// a value captured at startup: once a renewal lands in the store, the daemon stops
// reporting degraded without a restart, which is the whole point of the provider.
func TestExpiryCheckFollowsAReload(t *testing.T) {
	store := &fakeStore{version: 1, count: 1}
	cert, key := genCertExpiring(t, "a.example.com", time.Now().Add(3*24*time.Hour))
	store.certs = []directory.TLSCertData{{Name: "a.example.com", CertPEM: cert, KeyPEM: key}}
	p, err := New(nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := ExpiryCheck(p)
	if err := check.Probe(context.Background()); err == nil {
		t.Fatal("probe healthy with 3 days left, want degraded")
	}

	// The operator renews: a new certificate lands in the store and the version moves.
	cert, key = genCertExpiring(t, "a.example.com", time.Now().Add(90*24*time.Hour))
	store.certs = []directory.TLSCertData{{Name: "a.example.com", CertPEM: cert, KeyPEM: key}}
	store.version = 2
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := check.Probe(context.Background()); err != nil {
		t.Errorf("probe = %v after the renewal, want healthy without a restart", err)
	}
}

// TestExpiryIncludesTheFileCertificate proves the configuration-file fallback is
// judged too. It is served whenever a handshake finds no stored match, so an
// operator who never uploaded a certificate still gets the expiry warning.
func TestExpiryIncludesTheFileCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, err := tlstest.SelfSigned(dir) // valid for one hour
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(&config.Config{TLSCert: certPath, TLSKey: keyPath}, &fakeStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	notAfter, ok := p.Expiry()
	if !ok {
		t.Fatal("Expiry reported no certificate, want the configuration-file one")
	}
	if left := time.Until(notAfter); left <= 0 || left > 2*time.Hour {
		t.Errorf("time left = %v, want the file certificate's hour", left)
	}
	if err := ExpiryCheck(p).Probe(context.Background()); err == nil {
		t.Error("probe healthy with an hour left, want degraded inside the warning window")
	}
}
