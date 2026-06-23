package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/serve"
	"hermex/internal/tlscert"
)

// gatewayTLS selects the front door's certificate source from the stored TLS mode.
// In manual mode it is the poll-refreshed Provider (operator-uploaded certs in
// tls_certs plus the config-file fallback); in acme mode it is CertMagic obtaining
// and renewing Let's Encrypt certificates for the tenant allowlist via TLS-ALPN-01.
// It returns the source and a maintenance function to run in the background after
// serving starts — for acme the obtain must happen once the challenge listener is
// up, so it is never run inline here. A directory read failure falls back to manual
// rather than sinking the front door.
func gatewayTLS(cfg *config.Config, dir *directory.SQLDirectory, logger *logging.Logger) (serve.TLSSource, func(), error) {
	settings, _, err := dir.GetTLSSettings()
	if err != nil {
		if logger != nil {
			logger.Warn(logging.TLS, "settings.read", logging.Fields{"detail": "TLS settings unreadable; serving in manual mode", "error": err.Error()})
		}
		settings = directory.TLSSettings{Mode: "manual"}
	}

	if settings.Mode == "acme" {
		// CertMagic needs a writable directory for its account, certificate and lock
		// state. It defaults under DataDir, but the gateway mounts the mailbox root
		// read-only, so HERMEX_ACME_STORAGE points it at a dedicated writable mount.
		storage := os.Getenv("HERMEX_ACME_STORAGE")
		if storage == "" {
			storage = filepath.Join(cfg.DataDir, "acme")
		}
		acme, err := tlscert.NewACME(cfg, storage, settings, os.Getenv("HERMEX_ACME_CA_ROOT"), logger)
		if err != nil {
			return nil, nil, err
		}
		maintain := func() { acmeMaintain(acme, dir, cfg.Hostname, logger) }
		return acme, maintain, nil
	}

	provider, err := tlscert.New(cfg, dir, logger)
	if err != nil {
		return nil, nil, err
	}
	return provider, provider.RunMaintenance, nil
}

// acmeMaintain keeps the ACME certificates current and distributed. Each cycle it
// obtains certificates for the active tenant allowlist (CertMagic also renews
// managed certificates on its own, so this mainly covers a newly added domain) and
// then mirrors what it has into the tls_certs store. It runs after the gateway is
// serving so the TLS-ALPN-01 challenge reaches the live listener.
func acmeMaintain(acme *tlscert.ACMEProvider, dir *directory.SQLDirectory, hostname string, logger *logging.Logger) {
	cycle := func() {
		names, err := acmeNames(dir, hostname)
		if err != nil {
			if logger != nil {
				logger.Warn(logging.TLS, "acme.allowlist", logging.Fields{"detail": "could not read the domain allowlist", "error": err.Error()})
			}
			return
		}
		if err := acme.Manage(context.Background(), names); err != nil && logger != nil {
			logger.Warn(logging.TLS, "acme.manage", logging.Fields{"detail": "obtaining certificates failed; will retry on the next poll", "error": err.Error()})
		}
		mirrorACMECerts(acme, dir, names, logger)
	}
	cycle()
	tick := time.NewTicker(15 * time.Minute)
	defer tick.Stop()
	for range tick.C {
		cycle()
	}
}

// mirrorACMECerts copies each obtained certificate from CertMagic's storage into the
// tls_certs store, so the mail daemons — which terminate TLS on their own ports and
// only read that store — present the same Let's Encrypt certificate as the gateway.
// It is a reconcile, not an event hook: it handles both a fresh obtain and a renewal
// with one path, and writes a name only when its expiry differs from what the store
// already holds, so an unchanged certificate does not churn the daemons' poll.
func mirrorACMECerts(acme *tlscert.ACMEProvider, dir *directory.SQLDirectory, names []string, logger *logging.Logger) {
	existing, err := dir.ListTLSCerts()
	if err != nil {
		if logger != nil {
			logger.Warn(logging.TLS, "acme.mirror", logging.Fields{"detail": "could not read the certificate store", "error": err.Error()})
		}
		return
	}
	have := make(map[string]int64, len(existing))
	for _, c := range existing {
		have[c.Name] = c.NotAfter
	}
	for _, name := range names {
		certPEM, keyPEM, notAfter, ok, err := acme.LoadObtainedCert(context.Background(), name)
		if err != nil {
			if logger != nil {
				logger.Warn(logging.TLS, "acme.mirror", logging.Fields{"detail": "could not read an obtained certificate", "name": name, "error": err.Error()})
			}
			continue
		}
		if !ok || have[name] == notAfter {
			continue
		}
		if err := dir.SetTLSCert(name, string(certPEM), string(keyPEM), notAfter); err != nil {
			if logger != nil {
				logger.Warn(logging.TLS, "acme.mirror", logging.Fields{"detail": "could not store a mirrored certificate", "name": name, "error": err.Error()})
			}
			continue
		}
		if logger != nil {
			logger.Info(logging.TLS, "acme.mirror", logging.Fields{"detail": "mirrored an ACME certificate to the store", "name": name})
		}
	}
}

// acmeNames is the host-name allowlist the gateway obtains certificates for, read
// from the directory. Only active domains are included — a suspended domain
// (domain_status != 0) is not served, and obtaining a certificate for it would waste
// the CA's per-account rate limit.
func acmeNames(dir *directory.SQLDirectory, hostname string) ([]string, error) {
	domains, err := dir.ListDomains()
	if err != nil {
		return nil, err
	}
	active := make([]string, 0, len(domains))
	for _, d := range domains {
		if d.Status == 0 {
			active = append(active, d.Name)
		}
	}
	return expandACMENames(active, hostname), nil
}

// expandACMENames builds the certificate name set: the server's own hostname plus,
// for each domain, the mail/autodiscover/autoconfig hosts the owner points at the
// server (the prescribed CNAMEs). The apex is deliberately excluded — it is the
// tenant's own website, not the mail front door, so it would not resolve here and
// TLS-ALPN-01 would fail for it. Each included name must resolve to the gateway on
// :443. The result is deduplicated and sorted so the obtain order is stable.
func expandACMENames(domains []string, hostname string) []string {
	set := map[string]bool{}
	if hostname != "" {
		set[hostname] = true
	}
	for _, d := range domains {
		if d == "" {
			continue
		}
		set["mail."+d] = true
		set["autodiscover."+d] = true
		set["autoconfig."+d] = true
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
