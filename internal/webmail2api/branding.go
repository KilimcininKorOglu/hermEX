package webmail2api

import (
	"net/http"
	"strings"

	"hermex/internal/buildinfo"
	"hermex/internal/directory"
)

// brandingStore is the optional directory capability for per-domain login branding.
// SQLDirectory implements it; absent (static accounts) serves only the global
// default.
type brandingStore interface {
	GetDomainBranding(domain string) (directory.DomainBranding, bool, error)
}

// handleBranding serves the login-page branding. Unauthenticated: the SPA calls it
// before sign-in with the accessed domain, so a tenant sees its own name and colours
// on the login screen. A domain with no branding set (or no directory) gets the
// global default.
//
// The response also carries the build stamp, which the login footer renders. That is
// the only version this project has: there are no release tags, so the commit the
// binary was built from is the honest answer. It is not tenant-configurable, so a
// tenant cannot claim to run a build it is not running.
func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"app_name":      "hermEX",
		"primary_color": "#4f46e5",
		"tagline":       "Secure self-hosted email",
		"footer_text":   "hermEX",
		"version":       buildinfo.Revision(),
	}
	host := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
	if store, ok := s.auth.(brandingStore); ok && host != "" {
		if b, ok := resolveDomainBranding(store, host); ok {
			if b.AppName != "" {
				out["app_name"] = b.AppName
			}
			if b.LogoURL != "" {
				out["logo_url"] = b.LogoURL
			}
			if b.PrimaryColor != "" {
				out["primary_color"] = b.PrimaryColor
			}
			if b.Tagline != "" {
				out["tagline"] = b.Tagline
			}
			if b.FooterText != "" {
				out["footer_text"] = b.FooterText
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveDomainBranding maps an accessed hostname to a registered domain's branding,
// trying the host then progressively broader parents (mail.acme.com -> acme.com) and
// returning the first that has branding set.
func resolveDomainBranding(store brandingStore, host string) (directory.DomainBranding, bool) {
	for h := host; h != ""; {
		if b, has, err := store.GetDomainBranding(h); err == nil && has {
			return b, true
		}
		i := strings.Index(h, ".")
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	return directory.DomainBranding{}, false
}
