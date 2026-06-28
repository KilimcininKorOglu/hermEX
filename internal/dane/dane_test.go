package dane

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

// issue makes a certificate (self-signed when parent is nil, else signed by
// parent) carrying dnsName as a SAN, returning the parsed cert and its key.
func issue(t *testing.T, cn, dnsName string, isCA bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if dnsName != "" {
		tmpl.DNSNames = []string{dnsName}
	}
	if isCA {
		tmpl.IsCA = true
		tmpl.BasicConstraintsValid = true
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func sha256d(b []byte) []byte { s := sha256.Sum256(b); return s[:] }
func sha512d(b []byte) []byte { s := sha512.Sum512(b); return s[:] }

// TestMatchDANEEE proves DANE-EE(3) authenticates the leaf by digest with NO
// name check (the defining property: a TLSA match binds the key directly, so the
// SAN is irrelevant), and that a wrong digest does not match.
func TestMatchDANEEE(t *testing.T) {
	leaf, _ := issue(t, "mx.example.com", "mx.example.com", false, nil, nil)

	// selector=full-cert, SHA-256.
	eeCert := Record{Usage: usageDANEEE, Selector: selectorCert, MatchingType: matchSHA256, Data: sha256d(leaf.Raw)}
	if err := Match([]Record{eeCert}, []*x509.Certificate{leaf}, "mx.example.com"); err != nil {
		t.Errorf("DANE-EE full-cert SHA-256 should match: %v", err)
	}
	// selector=SPKI, SHA-512.
	eeSPKI := Record{Usage: usageDANEEE, Selector: selectorSPKI, MatchingType: matchSHA512, Data: sha512d(leaf.RawSubjectPublicKeyInfo)}
	if err := Match([]Record{eeSPKI}, []*x509.Certificate{leaf}, "mx.example.com"); err != nil {
		t.Errorf("DANE-EE SPKI SHA-512 should match: %v", err)
	}
	// DANE-EE skips the name check: a mismatched host still authenticates.
	if err := Match([]Record{eeCert}, []*x509.Certificate{leaf}, "totally-other.example.net"); err != nil {
		t.Errorf("DANE-EE must NOT name-check, but a mismatched host failed: %v", err)
	}
	// A wrong digest must not match.
	bad := Record{Usage: usageDANEEE, Selector: selectorCert, MatchingType: matchSHA256, Data: sha256d([]byte("not the cert"))}
	if err := Match([]Record{bad}, []*x509.Certificate{leaf}, "mx.example.com"); err == nil {
		t.Error("a non-matching DANE-EE digest must fail authentication")
	}
}

// TestMatchDANETA proves DANE-TA(2) authenticates by matching a trust anchor in
// the presented chain AND verifying the leaf chains to it AND a name check binds
// the leaf to the SMTP hostname (so a TA match alone, with a wrong name, fails).
func TestMatchDANETA(t *testing.T) {
	ca, caKey := issue(t, "Example CA", "", true, nil, nil)
	leaf, _ := issue(t, "mx.example.com", "mx.example.com", false, ca, caKey)
	chain := []*x509.Certificate{leaf, ca}

	taRec := Record{Usage: usageDANETA, Selector: selectorCert, MatchingType: matchSHA256, Data: sha256d(ca.Raw)}

	// Correct hostname: the leaf chains to the matched TA and the name binds.
	if err := Match([]Record{taRec}, chain, "mx.example.com"); err != nil {
		t.Errorf("DANE-TA should authenticate the chained leaf: %v", err)
	}
	// Wrong hostname: the TA still matches but the name check MUST reject (else a
	// CA-signed cert for any name would authenticate this server).
	if err := Match([]Record{taRec}, chain, "evil.example.com"); err == nil {
		t.Error("DANE-TA must name-check the leaf; a mismatched host must fail")
	}
	// A TA record whose digest matches nothing in the chain fails.
	noMatch := Record{Usage: usageDANETA, Selector: selectorCert, MatchingType: matchSHA256, Data: sha256d([]byte("unknown CA"))}
	if err := Match([]Record{noMatch}, chain, "mx.example.com"); err == nil {
		t.Error("a DANE-TA record matching no chain certificate must fail")
	}
}

// TestMatchUnusableRecords proves records outside the supported usage/selector/
// matching-type set are ignored, so a set containing only unusable records does
// not authenticate (the caller treats that as no-DANE, not a pass).
func TestMatchUnusableRecords(t *testing.T) {
	leaf, _ := issue(t, "mx.example.com", "mx.example.com", false, nil, nil)
	unusable := []Record{
		{Usage: 0, Selector: selectorCert, MatchingType: matchSHA256, Data: sha256d(leaf.Raw)},        // PKIX-TA
		{Usage: 1, Selector: selectorCert, MatchingType: matchSHA256, Data: sha256d(leaf.Raw)},        // PKIX-EE
		{Usage: usageDANEEE, Selector: selectorCert, MatchingType: 0, Data: leaf.Raw},                 // Full(0) matching type
		{Usage: usageDANEEE, Selector: 2, MatchingType: matchSHA256, Data: sha256d(leaf.Raw)},         // unknown selector
	}
	if err := Match(unusable, []*x509.Certificate{leaf}, "mx.example.com"); err == nil {
		t.Error("a record set of only unusable associations must not authenticate")
	}
}

// TestRecordUsable pins the usability filter that LookupTLSA applies before a
// record is ever trusted.
func TestRecordUsable(t *testing.T) {
	cases := []struct {
		rec  Record
		want bool
	}{
		{Record{Usage: usageDANEEE, Selector: selectorCert, MatchingType: matchSHA256}, true},
		{Record{Usage: usageDANETA, Selector: selectorSPKI, MatchingType: matchSHA512}, true},
		{Record{Usage: 0, Selector: selectorCert, MatchingType: matchSHA256}, false},  // PKIX usage
		{Record{Usage: usageDANEEE, Selector: 2, MatchingType: matchSHA256}, false},   // bad selector
		{Record{Usage: usageDANEEE, Selector: selectorCert, MatchingType: 0}, false},  // Full match type
	}
	for _, c := range cases {
		if got := c.rec.usable(); got != c.want {
			t.Errorf("usable(%+v) = %v, want %v", c.rec, got, c.want)
		}
	}
}

// TestMatchEmptyChain proves an empty certificate chain is a hard failure, never
// a silent pass.
func TestMatchEmptyChain(t *testing.T) {
	rec := Record{Usage: usageDANEEE, Selector: selectorCert, MatchingType: matchSHA256, Data: []byte{0x00}}
	if err := Match([]Record{rec}, nil, "mx.example.com"); err == nil {
		t.Error("an empty certificate chain must fail authentication")
	}
}

// TestResolverAddr proves a bare host gets the default DNS port while an explicit
// host:port is left intact.
func TestResolverAddr(t *testing.T) {
	if got := (&Resolver{Addr: "127.0.0.1"}).addr(); got != "127.0.0.1:53" {
		t.Errorf("bare host addr = %q, want 127.0.0.1:53", got)
	}
	if got := (&Resolver{Addr: "10.0.0.1:5353"}).addr(); got != "10.0.0.1:5353" {
		t.Errorf("host:port addr = %q, want 10.0.0.1:5353", got)
	}
}
