package tlstest

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"slices"
	"testing"
	"time"
)

// TestSelfSigned proves the helper produces a certificate the daemon TLS tests can
// actually serve with: the written PEM pair loads, the leaf is currently valid, and it
// covers localhost and 127.0.0.1. The coverage is load-bearing, a test dialing
// 127.0.0.1 with SNI "localhost" must get a matching certificate, so a regression that
// dropped either name would silently break every daemon TLS test that depends on it.
func TestSelfSigned(t *testing.T) {
	leaf := loadSelfSigned(t)

	if !currentlyValid(leaf, time.Now()) {
		t.Errorf("certificate not currently valid: NotBefore=%s NotAfter=%s", leaf.NotBefore, leaf.NotAfter)
	}
	wantCovers(t, leaf, "localhost")
	wantCovers(t, leaf, "127.0.0.1")
	if !hasIP(leaf, "127.0.0.1") {
		t.Errorf("IPAddresses = %v, want 127.0.0.1 present", leaf.IPAddresses)
	}
}

// loadSelfSigned generates a pair, loads it the way a serving daemon would, and
// returns the leaf certificate.
func loadSelfSigned(t *testing.T) *x509.Certificate {
	t.Helper()
	certPath, keyPath, err := SelfSigned(t.TempDir())
	mustNoErr(t, err, "SelfSigned")
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	mustNoErr(t, err, "the generated pair does not load")
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	mustNoErr(t, err, "parse leaf")
	return leaf
}

// currentlyValid reports whether the leaf's validity window covers the instant.
func currentlyValid(leaf *x509.Certificate, now time.Time) bool {
	return !now.Before(leaf.NotBefore) && !now.After(leaf.NotAfter)
}

// wantCovers fails the test unless the certificate covers the name.
func wantCovers(t *testing.T, leaf *x509.Certificate, name string) {
	t.Helper()
	if err := leaf.VerifyHostname(name); err != nil {
		t.Errorf("certificate does not cover %s: %v", name, err)
	}
}

// hasIP reports whether the address is one of the certificate's IP SANs.
func hasIP(leaf *x509.Certificate, addr string) bool {
	want := net.ParseIP(addr)
	return slices.ContainsFunc(leaf.IPAddresses, func(got net.IP) bool { return got.Equal(want) })
}

// mustNoErr stops the test when a step failed.
func mustNoErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}
