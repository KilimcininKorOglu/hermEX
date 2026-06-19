package admin

import (
	"encoding/json"
	"net/http"
)

// resolveUser resolves an {email} path value to a user id, writing a 404 (and
// returning ok=false) when no such user exists.
func (s *Server) resolveUser(w http.ResponseWriter, r *http.Request) (int64, bool) {
	uid, found, err := s.dir.UserID(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return 0, false
	}
	if !found {
		http.Error(w, "no such user", http.StatusNotFound)
		return 0, false
	}
	return uid, true
}

// roleRequest is the body of a grant or revoke.
type roleRequest struct {
	Role    string `json:"role"`
	ScopeID int64  `json:"scopeID"`
}

// handleListRoles lists a user's admin roles (system administrators only).
func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.resolveUser(w, r)
	if !ok {
		return
	}
	roles, err := s.dir.AdminRoles(uid)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, roles)
}

// handleGrantRole grants a user an admin role (system administrators only).
func (s *Server) handleGrantRole(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.resolveUser(w, r)
	if !ok {
		return
	}
	var req roleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Role == "" {
		http.Error(w, "a role is required", http.StatusBadRequest)
		return
	}
	if err := s.dir.GrantAdminRole(uid, req.Role, req.ScopeID); err != nil {
		http.Error(w, "could not grant role: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRevokeRole removes a user's admin role (system administrators only).
func (s *Server) handleRevokeRole(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.resolveUser(w, r)
	if !ok {
		return
	}
	var req roleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Role == "" {
		http.Error(w, "a role is required", http.StatusBadRequest)
		return
	}
	if err := s.dir.RevokeAdminRole(uid, req.Role, req.ScopeID); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
