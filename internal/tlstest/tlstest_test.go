package tlstest

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"
)

// TestSelfSigned proves the helper produces a certificate the daemon TLS tests can
// actually serve with: the written PEM pair loads, the leaf is currently valid, and it
// covers localhost and 127.0.0.1. The coverage is load-bearing — a test dialing
// 127.0.0.1 with SNI "localhost" must get a matching certificate, so a regression that
// dropped either name would silently break every daemon TLS test that depends on it.
func TestSelfSigned(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, err := SelfSigned(dir)
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}

	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("the generated pair does not load: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		t.Errorf("certificate not currently valid: NotBefore=%s NotAfter=%s", leaf.NotBefore, leaf.NotAfter)
	}
	if err := leaf.VerifyHostname("localhost"); err != nil {
		t.Errorf("certificate does not cover localhost: %v", err)
	}
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("certificate does not cover 127.0.0.1: %v", err)
	}
	var has127 bool
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			has127 = true
		}
	}
	if !has127 {
		t.Errorf("IPAddresses = %v, want 127.0.0.1 present", leaf.IPAddresses)
	}
}
