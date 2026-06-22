package tlscert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/caddyserver/certmagic"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// ACMEProvider serves certificates that CertMagic obtains and renews from an ACME
// CA (Let's Encrypt by default) via the TLS-ALPN-01 challenge. It is the gateway's
// certificate source in "acme" mode, satisfying serve.TLSSource so the front door
// uses it in place of the manual Provider. Names are managed proactively (not
// on-demand): the gateway obtains the full tenant allowlist up front so a name that
// only ever sees mail traffic — which never reaches this HTTPS listener — is still
// covered, and the certificate exists before the first client connects.
type ACMEProvider struct {
	magic  *certmagic.Config
	issuer *certmagic.ACMEIssuer // kept for its storage IssuerKey when mirroring certs
	logger *logging.Logger
}

// NewACME builds an ACME-managed certificate source. storageDir holds CertMagic's
// account, certificate and lock state (its built-in FileStorage, which provides the
// correct cross-process locking). The CA directory, account email and ToS agreement
// come from settings; an empty CA URL uses CertMagic's default (Let's Encrypt
// production). caRootFile, when set, is a PEM bundle of additional roots trusted for
// the ACME endpoint itself — needed only against a private/test CA (pebble) whose
// API serves a self-signed certificate; production CAs use publicly trusted TLS and
// leave it empty. logger may be nil.
func NewACME(storageDir string, settings directory.TLSSettings, caRootFile string, logger *logging.Logger) (*ACMEProvider, error) {
	var magic *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			return magic, nil
		},
	})
	magic = certmagic.New(cache, certmagic.Config{
		Storage: &certmagic.FileStorage{Path: storageDir},
	})

	acme := certmagic.ACMEIssuer{
		CA:                   settings.ACMECAURL,
		Email:                settings.ACMEEmail,
		Agreed:               settings.ACMEAgreed,
		DisableHTTPChallenge: true, // the gateway terminates TLS, so TLS-ALPN-01 is the fit
	}
	if caRootFile != "" {
		pool, err := loadACMERoots(caRootFile)
		if err != nil {
			return nil, err
		}
		acme.TrustedRoots = pool
	}
	issuer := certmagic.NewACMEIssuer(magic, acme)
	magic.Issuers = []certmagic.Issuer{issuer}

	return &ACMEProvider{magic: magic, issuer: issuer, logger: logger}, nil
}

// TLSEnabled always reports true: an ACME provider is constructed only when the
// operator has chosen acme mode, and its job is to terminate TLS.
func (p *ACMEProvider) TLSEnabled() bool { return true }

// TLSConfig returns a tls.Config whose GetCertificate resolves per handshake from
// CertMagic's managed set and also answers the TLS-ALPN-01 challenge, with HTTP/2
// advertised first for the gateway's HTTP traffic.
func (p *ACMEProvider) TLSConfig() (*tls.Config, error) {
	tc := p.magic.TLSConfig()
	tc.NextProtos = append([]string{"h2", "http/1.1"}, tc.NextProtos...)
	return tc, nil
}

// Manage obtains certificates for names that are missing and renews any that are
// near expiry, blocking until done. The gateway calls it at startup and whenever the
// tenant allowlist grows, so coverage tracks the domain set.
func (p *ACMEProvider) Manage(ctx context.Context, names []string) error {
	return p.magic.ManageSync(ctx, names)
}

// LoadObtainedCert reads the obtained certificate chain and private key for domain
// straight from CertMagic's storage as PEM, with the leaf's expiry in Unix millis.
// ok is false when the certificate has not been obtained yet, so the caller skips it
// without treating it as an error. Returning the stored PEM verbatim is deliberate:
// re-encoding a parsed private key would be brittle across key types, and the gateway
// only needs to copy the bytes to the store the mail daemons read.
func (p *ACMEProvider) LoadObtainedCert(ctx context.Context, domain string) (certPEM, keyPEM []byte, notAfter int64, ok bool, err error) {
	issuerKey := p.issuer.IssuerKey()
	certPEM, err = p.magic.Storage.Load(ctx, certmagic.StorageKeys.SiteCert(issuerKey, domain))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, 0, false, nil
	}
	if err != nil {
		return nil, nil, 0, false, err
	}
	keyPEM, err = p.magic.Storage.Load(ctx, certmagic.StorageKeys.SitePrivateKey(issuerKey, domain))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, 0, false, nil
	}
	if err != nil {
		return nil, nil, 0, false, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil, 0, false, fmt.Errorf("tlscert: stored certificate for %q is not PEM", domain)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, 0, false, fmt.Errorf("tlscert: parse stored certificate for %q: %w", domain, err)
	}
	return certPEM, keyPEM, leaf.NotAfter.UnixMilli(), true, nil
}

// loadACMERoots reads a PEM bundle into a certificate pool for trusting a private
// ACME endpoint.
func loadACMERoots(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("tlscert: read ACME CA roots: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("tlscert: no certificates parsed from %s", path)
	}
	return pool, nil
}
