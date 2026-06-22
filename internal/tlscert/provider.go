// Package tlscert supplies TLS serving certificates to the hermEX listeners from
// the directory's certificate store, refreshed on a poll so an operator's uploaded
// certificate — or a renewal — applies without restarting the daemon. It falls
// back to the configuration-file certificate when the store has no match, so a
// deployment that sets tls_cert/tls_key keeps working unchanged.
package tlscert

import (
	"crypto/tls"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/logging"
)

// CertStore is the certificate persistence the provider reads from;
// *directory.SQLDirectory satisfies it.
type CertStore interface {
	LoadTLSCerts() ([]directory.TLSCertData, error)
	TLSCertVersion() (version, count int64, err error)
}

// snapshot is the parsed certificate set served at a point in time, keyed by
// lowercased SNI name ("" is the default). It is replaced atomically on refresh.
type snapshot struct {
	byName map[string]*tls.Certificate
}

// Provider resolves a certificate per TLS handshake (by SNI) from a poll-refreshed
// snapshot of the store, falling back to the configuration-file certificate. It
// satisfies the serve.TLSSource interface so a listener uses it in place of the
// static config certificate.
type Provider struct {
	store    CertStore
	logger   *logging.Logger
	fileCert *tls.Certificate // configuration-file fallback, parsed once; nil if none
	snap     atomic.Pointer[snapshot]
	ver      int64 // last store version seen by refresh (only touched by refresh)
	cnt      int64 // last store row count seen by refresh
}

// New builds a provider over store, with cfg supplying the configuration-file
// fallback certificate (when cfg.TLSEnabled()). A file-certificate parse error is
// fatal — it mirrors the previous startup behaviour — but a store read failure is
// not: the provider serves the file certificate and the poll retries, so a
// momentarily unreachable database never stops TLS from coming up. logger may be
// nil to disable the provider's own logging.
func New(cfg *config.Config, store CertStore, logger *logging.Logger) (*Provider, error) {
	p := &Provider{store: store, logger: logger, ver: -1, cnt: -1}
	if cfg != nil && cfg.TLSEnabled() {
		tc, err := cfg.TLSConfig()
		if err != nil {
			return nil, err
		}
		if len(tc.Certificates) > 0 {
			p.fileCert = &tc.Certificates[0]
		}
	}
	p.snap.Store(&snapshot{byName: map[string]*tls.Certificate{}})
	if err := p.Refresh(); err != nil {
		p.warn("initial certificate load failed; serving the file certificate until the store is reachable", err)
	}
	return p, nil
}

// TLSEnabled reports whether the provider can present any certificate — a stored
// one or the configuration-file fallback. A listener consults it once at startup
// to choose TLS over plaintext; switching a plaintext listener to TLS still needs
// a restart.
func (p *Provider) TLSEnabled() bool {
	if p.fileCert != nil {
		return true
	}
	return len(p.snap.Load().byName) > 0
}

// TLSConfig returns a tls.Config whose GetCertificate resolves per handshake, so a
// certificate replaced in the store is served without rebuilding the listener.
func (p *Provider) TLSConfig() (*tls.Config, error) {
	return &tls.Config{GetCertificate: p.getCertificate, MinVersion: tls.VersionTLS12}, nil
}

// getCertificate picks the certificate for a handshake: an exact SNI match in the
// store, else the store default (the "" name), else the configuration-file
// fallback. A stored certificate takes precedence over the file so an upload
// overrides the configured one.
func (p *Provider) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	byName := p.snap.Load().byName
	if c, ok := byName[strings.ToLower(hello.ServerName)]; ok {
		return c, nil
	}
	if c, ok := byName[""]; ok {
		return c, nil
	}
	if p.fileCert != nil {
		return p.fileCert, nil
	}
	return nil, fmt.Errorf("tlscert: no certificate for server name %q", hello.ServerName)
}

// Refresh reloads the snapshot from the store when its version probe has moved
// since the last load. A row that fails to parse is skipped (and logged) so one
// bad upload cannot drop the certificates that do parse.
func (p *Provider) Refresh() error {
	ver, cnt, err := p.store.TLSCertVersion()
	if err != nil {
		return err
	}
	if ver == p.ver && cnt == p.cnt {
		return nil
	}
	rows, err := p.store.LoadTLSCerts()
	if err != nil {
		return err
	}
	byName := make(map[string]*tls.Certificate, len(rows))
	for _, r := range rows {
		cert, err := tls.X509KeyPair([]byte(r.CertPEM), []byte(r.KeyPEM))
		if err != nil {
			p.warn("skipping an unparseable stored certificate", fmt.Errorf("name %q: %w", r.Name, err))
			continue
		}
		byName[strings.ToLower(r.Name)] = &cert
	}
	p.snap.Store(&snapshot{byName: byName})
	p.ver, p.cnt = ver, cnt
	return nil
}

// RunMaintenance polls the store every minute and refreshes the snapshot when it
// changes, so an admin's upload or renewal applies without a restart. It runs
// until the process exits, mirroring the other settings-reload loops.
func (p *Provider) RunMaintenance() {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		if err := p.Refresh(); err != nil {
			p.warn("certificate refresh failed; keeping the current certificates", err)
		}
	}
}

// warn emits a TLS-subsystem warning when a logger is configured.
func (p *Provider) warn(detail string, err error) {
	if p.logger == nil {
		return
	}
	p.logger.Warn(logging.TLS, "certificate.reload", logging.Fields{"detail": detail, "error": err.Error()})
}
