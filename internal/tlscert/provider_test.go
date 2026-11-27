package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/config"
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
	certs      []directory.TLSCertData
	version    int64
	count      int64
	loads      int
	versionErr error
	loadErr    error
}

func (s *fakeStore) LoadTLSCerts() ([]directory.TLSCertData, error) {
	s.loads++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.certs, nil
}
func (s *fakeStore) TLSCertVersion() (int64, int64, error) {
	if s.versionErr != nil {
		return 0, 0, s.versionErr
	}
	return s.version, s.count, nil
}

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

	if got, err := p.getCertificate(helloFor("mail.example.com")); err != nil || got != named {
		t.Errorf("exact SNI match served (%v, %v), want named certificate", got, err)
	}
	if got, err := p.getCertificate(helloFor("other.example.com")); err != nil || got != def {
		t.Errorf("unknown SNI with a default served (%v, %v), want default", got, err)
	}

	// No store default: an unknown name falls through to the file fallback.
	p.snap.Store(&snapshot{byName: map[string]*tls.Certificate{"mail.example.com": named}})
	if got, err := p.getCertificate(helloFor("other.example.com")); err != nil || got != file {
		t.Errorf("unknown SNI with no default served (%v, %v), want file fallback", got, err)
	}

	// No store certs and no file fallback: an error, not a nil certificate.
	p.fileCert = nil
	p.snap.Store(&snapshot{byName: map[string]*tls.Certificate{}})
	got, err := p.getCertificate(helloFor("x"))
	if err == nil {
		t.Errorf("with no certificate available getCertificate must error, not return nil")
	}
	if got != nil {
		t.Errorf("with no certificate available getCertificate returned non-nil cert %v", got)
	}
	if len(p.snap.Load().byName) != 0 {
		t.Errorf("refusal mutated snapshot: len = %d, want 0", len(p.snap.Load().byName))
	}
}

// TestServerNameMatchIgnoresCase stores a certificate under a mixed-case name and
// asks for it in another case. RFC 6066 3 and RFC 4343 make a host name
// case-insensitive, so the named certificate must be served, not the default
// that a byte-exact match would fall through to.
func TestServerNameMatchIgnoresCase(t *testing.T) {
	cert, key := genCertPEM(t, "mail.example.com")
	defCert, defKey := genCertPEM(t, "default.example.com")
	store := &fakeStore{
		certs: []directory.TLSCertData{
			{Name: "", CertPEM: defCert, KeyPEM: defKey},
			{Name: "Mail.Example.com", CertPEM: cert, KeyPEM: key},
		},
		version: 1, count: 2,
	}
	p, err := New(nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	def, err := p.getCertificate(helloFor("unknown.example.com"))
	if err != nil {
		t.Fatalf("default lookup failed: %v", err)
	}
	for _, sni := range []string{"mail.example.com", "MAIL.EXAMPLE.COM", "mail.EXAMPLE.com"} {
		got, err := p.getCertificate(helloFor(sni))
		if err != nil || got == nil || got == def {
			t.Errorf("SNI %q served the default (or nothing), want the named certificate", sni)
		}
	}
}

// TestProviderRefreshReloadsOnVersionChange proves the poll reloads only when the
// store's version probe moves: an unchanged probe skips the (re-parsing) load, and
// a bumped probe picks up the new material, so a renewal applies without a restart
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
	wantServed(t, p, "anything", nil)

	// Same version: the row set could change but the provider must not re-load.
	store.certs = nil
	mustRefresh(t, p)
	if store.loads != 1 {
		t.Errorf("unchanged version must skip the load, got %d loads", store.loads)
	}

	// Bumped version: now it reloads and sees the new set.
	cert2, key2 := genCertPEM(t, "new.example.com")
	store.certs = []directory.TLSCertData{{Name: "new.example.com", CertPEM: cert2, KeyPEM: key2}}
	store.version, store.count = 2, 1
	mustRefresh(t, p)
	if store.loads != 2 {
		t.Errorf("bumped version must reload, got %d loads", store.loads)
	}
	wantServed(t, p, "new.example.com", nil)
}

// mustRefresh runs one Refresh and fails the test on its error.
func mustRefresh(t *testing.T, p *Provider) {
	t.Helper()
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
}

// wantServed requires sni to be served without error: by want when want is set,
// by any certificate otherwise.
func wantServed(t *testing.T, p *Provider, sni string, want *tls.Certificate) {
	t.Helper()
	got, err := p.getCertificate(helloFor(sni))
	if err != nil || got == nil || (want != nil && got != want) {
		t.Errorf("%q served (%v, %v), want %v", sni, got, err, want)
	}
}

// wantStamp requires the provider's recorded store version and row count.
func wantStamp(t *testing.T, p *Provider, label string, ver, cnt int64) {
	t.Helper()
	if p.ver != ver || p.cnt != cnt {
		t.Errorf("%s: ver=%d cnt=%d, want %d/%d", label, p.ver, p.cnt, ver, cnt)
	}
}

// wantRefreshErr requires Refresh to return want.
func wantRefreshErr(t *testing.T, p *Provider, want error) {
	t.Helper()
	if err := p.Refresh(); !errors.Is(err, want) {
		t.Fatalf("Refresh = %v, want %v", err, want)
	}
}

// TestProviderRefreshSkipsBadRow proves one unparseable upload or mismatched key
// pair cannot take down the certificates that parse: the bad rows are refused and
// omitted from the snapshot while the good one still serves.
func TestProviderRefreshSkipsBadRow(t *testing.T) {
	good, goodKey := genCertPEM(t, "good.example.com")
	otherCert, _ := genCertPEM(t, "other.example.com")
	store := &fakeStore{
		certs: []directory.TLSCertData{
			{Name: "good.example.com", CertPEM: good, KeyPEM: goodKey},
			{Name: "bad.example.com", CertPEM: "not a pem", KeyPEM: "not a key"},
			{Name: "mismatch.example.com", CertPEM: otherCert, KeyPEM: goodKey},
		},
		version: 1, count: 3,
	}
	p, err := New(nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := p.getCertificate(helloFor("good.example.com")); err != nil || got == nil {
		t.Errorf("the parseable certificate must still be served despite bad sibling rows, got (%v, %v)", got, err)
	}
	for _, rejected := range []string{"bad.example.com", "mismatch.example.com"} {
		if got, err := p.getCertificate(helloFor(rejected)); err == nil || got != nil {
			t.Errorf("invalid stored cert %q must be refused, got (%v, %v)", rejected, got, err)
		}
	}
	if len(p.snap.Load().byName) != 1 {
		t.Errorf("snapshot has %d entries, want only the 1 valid cert", len(p.snap.Load().byName))
	}
}

// TestProviderNewRejectsInvalidFileCert proves New refuses missing or corrupt
// configuration-file certificate material rather than starting with a broken fallback.
func TestProviderNewRejectsInvalidFileCert(t *testing.T) {
	dir := t.TempDir()
	missingCert := filepath.Join(dir, "missing.crt")
	missingKey := filepath.Join(dir, "missing.key")
	if p, err := New(&config.Config{TLSCert: missingCert, TLSKey: missingKey}, &fakeStore{}, nil); err == nil || p != nil {
		t.Errorf("missing file cert must fail New, got (%v, %v)", p, err)
	}

	corruptCert := filepath.Join(dir, "corrupt.crt")
	corruptKey := filepath.Join(dir, "corrupt.key")
	if err := os.WriteFile(corruptCert, []byte("not-a-cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corruptKey, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := New(&config.Config{TLSCert: corruptCert, TLSKey: corruptKey}, &fakeStore{}, nil); err == nil || p != nil {
		t.Errorf("corrupt file cert must fail New, got (%v, %v)", p, err)
	}
}

// TestProviderRefreshErrorPreservesState proves a failing store probe or load
// returns the error without altering the live snapshot, version, or row count.
func TestProviderRefreshErrorPreservesState(t *testing.T) {
	cert1, key1 := genCertPEM(t, "v1.example.com")
	store := &fakeStore{
		certs:   []directory.TLSCertData{{Name: "v1.example.com", CertPEM: cert1, KeyPEM: key1}},
		version: 1, count: 1,
	}
	p, err := New(nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeCert, err := p.getCertificate(helloFor("v1.example.com"))
	if err != nil {
		t.Fatalf("initial cert lookup failed: %v", err)
	}

	// Version probe failure leaves snapshot, ver, and cnt unchanged.
	store.versionErr = errors.New("db version probe failed")
	wantRefreshErr(t, p, store.versionErr)
	wantStamp(t, p, "after a failed version probe", 1, 1)
	wantServed(t, p, "v1.example.com", beforeCert)
	store.versionErr = nil

	// Load failure on bumped version leaves snapshot, ver, and cnt unchanged, so
	// the next poll retries the load.
	store.version, store.count = 2, 1
	store.loadErr = errors.New("db load failed")
	wantRefreshErr(t, p, store.loadErr)
	wantStamp(t, p, "after a failed load", 1, 1)
	wantServed(t, p, "v1.example.com", beforeCert)

	// Clearing the error allows the bumped version to load cleanly.
	cert2, key2 := genCertPEM(t, "v2.example.com")
	store.loadErr = nil
	store.certs = []directory.TLSCertData{{Name: "v2.example.com", CertPEM: cert2, KeyPEM: key2}}
	mustRefresh(t, p)
	wantStamp(t, p, "after recovery", 2, 1)
	wantServed(t, p, "v2.example.com", nil)
}

// TestProviderRefusesHostileSNIWithoutFallback proves untrusted or malformed SNI
// values are refused when no default or file fallback exists, leaving the snapshot intact.
func TestProviderRefusesHostileSNIWithoutFallback(t *testing.T) {
	named := &tls.Certificate{}
	p := &Provider{}
	p.snap.Store(&snapshot{byName: map[string]*tls.Certificate{"mail.example.com": named}})

	hostile := []string{
		"",
		"../etc/passwd",
		"*.example.com",
		strings.Repeat("a", 300) + ".example.com",
		"other-tenant.example.org",
	}
	for _, sni := range hostile {
		got, err := p.getCertificate(helloFor(sni))
		if err == nil || got != nil {
			t.Errorf("hostile SNI %q must be refused, got (%v, %v)", sni, got, err)
		}
	}
	if len(p.snap.Load().byName) != 1 {
		t.Errorf("hostile SNI probes altered snapshot len to %d, want 1", len(p.snap.Load().byName))
	}
	if got, err := p.getCertificate(helloFor("mail.example.com")); err != nil || got != named {
		t.Errorf("valid SNI after hostile probes returned (%v, %v), want named cert", got, err)
	}
}
