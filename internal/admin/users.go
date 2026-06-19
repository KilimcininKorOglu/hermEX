package admin

import (
	"encoding/json"
	"net/http"
)

// handleListUsers lists every user. This first increment is system-admin only;
// org- and domain-scoped listing is a later refinement.
func (s *Server) handleListUsers(w http.ResponseWriter, _ *http.Request) {
	users, err := s.dir.ListUsers()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, users)
}

// handleCreateUser provisions a user (system administrators only); its maildir is
// derived from the configured data root. The domain must already exist.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Password == "" {
		http.Error(w, "an email and password are required", http.StatusBadRequest)
		return
	}
	id, err := s.dir.CreateUser(req.Email, req.Password, s.paths.MaildirFor(req.Email))
	if err != nil {
		http.Error(w, "could not create user: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"id": id, "email": req.Email})
}
