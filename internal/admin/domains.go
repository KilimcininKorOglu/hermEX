package admin

import (
	"net/http"

	"hermex/internal/directory"
)

// claimsOf returns the session claims a protected handler runs under.
func claimsOf(r *http.Request) claims {
	cl, _ := r.Context().Value(ctxKey{}).(claims)
	return cl
}

// isSystemAdmin reports whether a user holds the unrestricted system admin role.
func (s *Server) isSystemAdmin(userID int64) bool {
	roles, err := s.dir.AdminRoles(userID)
	if err != nil {
		return false
	}
	for _, role := range roles {
		if role.Role == directory.AdminSystem {
			return true
		}
	}
	return false
}

// requireSystem wraps a handler so it runs only for a system administrator;
// every other caller (including an org or domain admin) is refused.
func (s *Server) requireSystem(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isSystemAdmin(claimsOf(r).UserID) {
			http.Error(w, "forbidden: requires a system administrator", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// handleListDomains lists every domain (system administrators only — domains span
// organizations).
func (s *Server) handleListDomains(w http.ResponseWriter, _ *http.Request) {
	domains, err := s.dir.ListDomains()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, domains)
}
