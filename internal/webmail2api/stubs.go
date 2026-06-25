package webmail2api

import "net/http"

// This file holds endpoints with no server-side backing yet. They return a
// well-typed empty/default response (not the generic {} stub) so the SPA renders
// the page cleanly while the feature is built out.

// handleBranding serves the login-page branding. Unauthenticated.
func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"app_name":      "hermEX",
		"primary_color": "#4f46e5",
		"tagline":       "Secure self-hosted email",
		"footer_text":   "hermEX",
	})
}
