package admin

import (
	"encoding/json"
	"net/http"

	"hermex/internal/directory"
)

// claimsOf returns the session claims a protected handler runs under.
func claimsOf(r *http.Request) claims {
	cl, _ := r.Context().Value(ctxKey{}).(claims)
	return cl
}

// isSystemAdmin reports whether a user holds full (write) system authority,
// resolved through the single permission path: a SystemAdmin permission, whether
// granted by a named role or bridged from a direct system tier grant. A
// read-only system administrator is NOT a full administrator.
func (s *Server) isSystemAdmin(userID int64) bool {
	return hasPerm(s.adminPerms(userID), directory.PermSystemAdmin, "")
}

// requireSystem gates a handler on system authority, method-aware: a read
// (GET/HEAD) admits a read-only system administrator, while a state-changing
// method requires a full system administrator. This is the chokepoint that makes
// SystemAdminRO read-everything-write-nothing — it cannot be forgotten per
// handler.
func (s *Server) requireSystem(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := claimsOf(r).UserID
		if isReadMethod(r.Method) {
			if !s.isSystemReadAdmin(uid) {
				http.Error(w, "forbidden: requires a system administrator", http.StatusForbidden)
				return
			}
		} else if !s.isSystemAdmin(uid) {
			http.Error(w, "forbidden: requires a full system administrator", http.StatusForbidden)
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

// handleCreateDomain provisions a domain (system administrators only); its
// homedir is derived from the configured data root.
func (s *Server) handleCreateDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "a domain name is required", http.StatusBadRequest)
		return
	}
	id, err := s.dir.CreateDomain(req.Name, s.paths.HomedirFor(req.Name))
	if err != nil {
		http.Error(w, "could not create domain: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"id": id, "name": req.Name})
}
