package main

import (
	"io"
	"net/http"
	"strings"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/mtasts"
)

// mtastsDirectory is the slice of the directory the MTA-STS policy handler reads: the
// publishing settings and the domain list (to confirm a request is for an active
// local domain). *directory.SQLDirectory satisfies it.
type mtastsDirectory interface {
	GetMTASTSSettings() (directory.MTASTSSettings, bool, error)
	ListDomains() ([]directory.DomainInfo, error)
}

// mtastsPolicyPath is the well-known location a sending MTA fetches an MTA-STS policy
// from (RFC 8461 §3.3), under the per-domain policy host mta-sts.<domain>.
const mtastsPolicyPath = "/.well-known/mta-sts.txt"

// withMTASTS wraps next so a request to an MTA-STS policy host — Host mta-sts.<domain>
// for the well-known policy path — is answered locally with this server's published
// policy, while every other request is proxied to a backend. Publishing MTA-STS means
// serving this file over HTTPS, and the gateway is the TLS front door, so it is where
// the policy is served. A per-request directory failure answers 5xx rather than
// panicking, consistent with the front door's resilience model.
func withMTASTS(cfg *config.Config, dir mtastsDirectory, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		domain, isPolicyHost := strings.CutPrefix(strings.ToLower(host), "mta-sts.")
		if !isPolicyHost || r.URL.Path != mtastsPolicyPath {
			next.ServeHTTP(w, r)
			return
		}
		serveMTASTSPolicy(w, cfg, dir, domain)
	})
}

// serveMTASTSPolicy writes the published MTA-STS policy for domain, or 404 when
// publishing is disabled or domain is not an active local domain. The policy's single
// mx is this server's hostname — the MX target every domain's mail routes to — and the
// mode and max_age come from the operator's settings.
func serveMTASTSPolicy(w http.ResponseWriter, cfg *config.Config, dir mtastsDirectory, domain string) {
	settings, _, err := dir.GetMTASTSSettings()
	if err != nil {
		http.Error(w, "mta-sts unavailable", http.StatusInternalServerError)
		return
	}
	if !settings.Enabled {
		http.Error(w, "no MTA-STS policy", http.StatusNotFound)
		return
	}
	domains, err := dir.ListDomains()
	if err != nil {
		http.Error(w, "mta-sts unavailable", http.StatusInternalServerError)
		return
	}
	active := false
	for _, d := range domains {
		if d.Status == 0 && strings.EqualFold(d.Name, domain) {
			active = true
			break
		}
	}
	if !active {
		http.Error(w, "no MTA-STS policy", http.StatusNotFound)
		return
	}
	policy := mtasts.Policy{
		Mode:   mtasts.Mode(settings.Mode),
		MX:     []string{cfg.Hostname},
		MaxAge: settings.MaxAge,
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, mtasts.Build(policy))
}
