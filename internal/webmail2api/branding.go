package webmail2api

import (
	"net/http"
	"strings"

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
func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"app_name":      "hermEX",
		"primary_color": "#4f46e5",
		"tagline":       "Secure self-hosted email",
		"footer_text":   "hermEX",
	}
	if domain := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain"))); domain != "" {
		if store, ok := s.auth.(brandingStore); ok {
			if b, has, err := store.GetDomainBranding(domain); err == nil && has {
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
	}
	writeJSON(w, http.StatusOK, out)
}
