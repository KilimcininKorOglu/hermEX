package admin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// genCertKeyPEM returns a throwaway self-signed certificate (with the given SAN
// names and expiry) and its key, in PEM, for the validation and upload tests.
func genCertKeyPEM(t *testing.T, cn string, dnsNames []string, notAfter time.Time) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     dnsNames,
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

// TestValidateTLSCert proves the gate an upload must pass before it is stored: a
// matching pair within its validity is accepted and its expiry and SAN names are
// surfaced, while a mismatched key, an expired certificate, and garbage are each
// rejected — a bad certificate must never reach the listener.
func TestValidateTLSCert(t *testing.T) {
	cert, key := genCertKeyPEM(t, "mail.example.com", []string{"mail.example.com", "autodiscover.example.com"}, time.Now().Add(365*24*time.Hour))
	notAfter, dnsNames, err := validateTLSCert(cert, key)
	if err != nil {
		t.Fatalf("a valid pair was rejected: %v", err)
	}
	if notAfter <= time.Now().UnixMilli() {
		t.Errorf("notAfter = %d, want a future expiry", notAfter)
	}
	if strings.Join(dnsNames, ",") != "mail.example.com,autodiscover.example.com" {
		t.Errorf("dnsNames = %v, want the certificate's SAN host names", dnsNames)
	}

	// A key from a different certificate must not pass — pairing is the core check.
	_, otherKey := genCertKeyPEM(t, "other.example.com", nil, time.Now().Add(time.Hour))
	if _, _, err := validateTLSCert(cert, otherKey); err == nil {
		t.Error("a mismatched key was accepted; the pair check must reject it")
	}

	// An already-expired certificate must be rejected even though the pair is valid.
	expiredCert, expiredKey := genCertKeyPEM(t, "old.example.com", nil, time.Now().Add(-24*time.Hour))
	if _, _, err := validateTLSCert(expiredCert, expiredKey); err == nil {
		t.Error("an expired certificate was accepted; it must be rejected")
	}

	// Non-PEM input must error rather than panic.
	if _, _, err := validateTLSCert("not a cert", "not a key"); err == nil {
		t.Error("garbage input was accepted")
	}
}

// TestTLSCertUploadStoresAndDelete proves the panel flow end to end against the
// store: a valid upload is stored and confirmed, a garbage upload is rejected and
// changes nothing, and a delete removes the certificate. It guards the handler
// wiring and the template that the pure validation test cannot.
func TestTLSCertUploadStoresAndDelete(t *testing.T) {
	d := systemAdminDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	cert, key := genCertKeyPEM(t, "mail.example.com", []string{"mail.example.com"}, time.Now().Add(365*24*time.Hour))
	resp := htmxPOST(t, ts, "/admin/ui/tls/upload", session, csrf, url.Values{"name": {""}, "cert": {cert}, "key": {key}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Stored the default certificate") {
		t.Errorf("upload response missing the stored-confirmation; got: %s", body)
	}
	if len(d.tlsCerts) != 1 {
		t.Fatalf("store has %d certs after upload, want 1", len(d.tlsCerts))
	}

	// A garbage upload is rejected and leaves the store untouched.
	bad := htmxPOST(t, ts, "/admin/ui/tls/upload", session, csrf, url.Values{"cert": {"nope"}, "key": {"nope"}})
	badBody, _ := io.ReadAll(bad.Body)
	bad.Body.Close()
	if !strings.Contains(string(badBody), "Upload rejected") {
		t.Errorf("garbage upload was not rejected; got: %s", badBody)
	}
	if len(d.tlsCerts) != 1 {
		t.Errorf("garbage upload changed the store: %d certs", len(d.tlsCerts))
	}

	// Delete removes the stored certificate.
	del := htmxPOST(t, ts, "/admin/ui/tls/delete", session, csrf, url.Values{"name": {""}})
	del.Body.Close()
	if len(d.tlsCerts) != 0 {
		t.Errorf("delete left %d certs, want 0", len(d.tlsCerts))
	}
}

// TestTLSSettingsModeSwitch proves the panel saves the certificate mode the gateway
// reads at startup: acme mode requires an account email and ToS agreement (a save
// missing either is rejected and writes nothing, so the gateway never reaches out to
// a CA without consent), a complete acme save is stored, and manual mode is always
// accepted.
func TestTLSSettingsModeSwitch(t *testing.T) {
	d := systemAdminDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	// acme without agreeing to the terms is rejected and changes nothing.
	bad := htmxPOST(t, ts, "/admin/ui/tls/mode", session, csrf, url.Values{"mode": {"acme"}, "acme_email": {"ops@example.com"}})
	bb, _ := io.ReadAll(bad.Body)
	bad.Body.Close()
	if !strings.Contains(string(bb), "terms of service") {
		t.Errorf("acme without agreement was not rejected; got: %s", bb)
	}
	if d.tlsSettings != nil {
		t.Errorf("a rejected save still wrote settings: %+v", d.tlsSettings)
	}

	// A complete acme save is stored verbatim.
	good := htmxPOST(t, ts, "/admin/ui/tls/mode", session, csrf, url.Values{
		"mode": {"acme"}, "acme_email": {"ops@example.com"},
		"acme_ca_url": {"https://pebble:14000/dir"}, "acme_agreed": {"on"},
	})
	good.Body.Close()
	if d.tlsSettings == nil || d.tlsSettings.Mode != "acme" || d.tlsSettings.ACMEEmail != "ops@example.com" || !d.tlsSettings.ACMEAgreed {
		t.Fatalf("acme save = %+v, want acme mode carrying the email and agreement", d.tlsSettings)
	}

	// Manual mode is always accepted (no ACME requirements).
	man := htmxPOST(t, ts, "/admin/ui/tls/mode", session, csrf, url.Values{"mode": {"manual"}})
	man.Body.Close()
	if d.tlsSettings.Mode != "manual" {
		t.Errorf("manual save mode = %q, want manual", d.tlsSettings.Mode)
	}
}
