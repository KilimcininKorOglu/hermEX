package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"hermex/internal/directory"
)

// genCertPEM returns a throwaway self-signed certificate and key in PEM, so the
// provider's parse-and-serve path runs against real material.
func genCertPEM(t *testing.T, cn string) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(1<<31, 0),
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

// fakeStore is an in-memory CertStore whose version probe and rows the test drives;
// loads counts LoadTLSCerts calls so the version-skip optimization is observable.
type fakeStore struct {
	certs   []directory.TLSCertData
	version int64
	count   int64
	loads   int
}

func (s *fakeStore) LoadTLSCerts() ([]directory.TLSCertData, error) {
	s.loads++
	return s.certs, nil
}
func (s *fakeStore) TLSCertVersion() (int64, int64, error) { return s.version, s.count, nil }

// helloFor builds a ClientHelloInfo for an SNI server name.
func helloFor(name string) *tls.ClientHelloInfo { return &tls.ClientHelloInfo{ServerName: name} }

// TestProviderResolutionOrder proves the SNI resolution a listener depends on: an
// exact name wins, an unknown name with a default falls to the default, an unknown
// name with no default but a file fallback serves the file, and nothing at all is
// an error rather than a silent nil. Getting this order wrong serves the wrong
// certificate (or none) for a host, so each branch is pinned by identity.
func TestProviderResolutionOrder(t *testing.T) {
	def := &tls.Certificate{}
	named := &tls.Certificate{}
	file := &tls.Certificate{}

	p := &Provider{fileCert: file}
	p.snap.Store(&snapshot{byName: map[string]*tls.Certificate{"": def, "mail.example.com": named}})

	if got, _ := p.getCertificate(helloFor("mail.example.com")); got != named {
		t.Errorf("exact SNI match served the wrong certificate")
	}
	if got, _ := p.getCertificate(helloFor("other.example.com")); got != def {
		t.Errorf("unknown SNI with a default must serve the default")
	}

	// No store default: an unknown name falls through to the file fallback.
	p.snap.Store(&snapshot{byName: map[string]*tls.Certificate{"mail.example.com": named}})
	if got, _ := p.getCertificate(helloFor("other.example.com")); got != file {
		t.Errorf("unknown SNI with no default must serve the file fallback")
	}

	// No store certs and no file fallback: an error, not a nil certificate.
	p.fileCert = nil
	p.snap.Store(&snapshot{byName: map[string]*tls.Certificate{}})
	if _, err := p.getCertificate(helloFor("x")); err == nil {
		t.Errorf("with no certificate available getCertificate must error, not return nil")
	}
}

// TestProviderRefreshReloadsOnVersionChange proves the poll reloads only when the
// store's version probe moves: an unchanged probe skips the (re-parsing) load, and
// a bumped probe picks up the new material — so a renewal applies without a restart
// while idle polls stay cheap.
func TestProviderRefreshReloadsOnVersionChange(t *testing.T) {
	cert, key := genCertPEM(t, "mail.example.com")
	store := &fakeStore{
		certs:   []directory.TLSCertData{{Name: "", CertPEM: cert, KeyPEM: key}},
		version: 1, count: 1,
	}
	p, err := New(nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if store.loads != 1 {
		t.Fatalf("New should load once, got %d loads", store.loads)
	}
	if got, _ := p.getCertificate(helloFor("anything")); got == nil {
		t.Fatal("after load the default certificate must be served")
	}

	// Same version: the row set could change but the provider must not re-load.
	store.certs = nil
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if store.loads != 1 {
		t.Errorf("unchanged version must skip the load, got %d loads", store.loads)
	}

	// Bumped version: now it reloads and sees the empty set.
	cert2, key2 := genCertPEM(t, "new.example.com")
	store.certs = []directory.TLSCertData{{Name: "new.example.com", CertPEM: cert2, KeyPEM: key2}}
	store.version, store.count = 2, 1
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if store.loads != 2 {
		t.Errorf("bumped version must reload, got %d loads", store.loads)
	}
	if got, _ := p.getCertificate(helloFor("new.example.com")); got == nil {
		t.Error("after reload the new certificate must be served")
	}
}

// TestProviderRefreshSkipsBadRow proves one unparseable upload cannot take down the
// certificates that parse: the bad row is dropped and the good one still serves.
func TestProviderRefreshSkipsBadRow(t *testing.T) {
	good, goodKey := genCertPEM(t, "good.example.com")
	store := &fakeStore{
		certs: []directory.TLSCertData{
			{Name: "good.example.com", CertPEM: good, KeyPEM: goodKey},
			{Name: "bad.example.com", CertPEM: "not a pem", KeyPEM: "not a key"},
		},
		version: 1, count: 2,
	}
	p, err := New(nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := p.getCertificate(helloFor("good.example.com")); got == nil {
		t.Error("the parseable certificate must still be served despite a bad sibling row")
	}
}
