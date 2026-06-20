package admin

import (
	"encoding/json"
	"net/http"

	"hermex/internal/directory"
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

// handleSetPassword replaces a user's local password (system administrators
// only). The user is named in the path; the new password is the request body.
func (s *Server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		http.Error(w, "a password is required", http.StatusBadRequest)
		return
	}
	found, err := s.dir.SetPassword(r.PathValue("email"), req.Password)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetUser returns a single user's administrative record (system
// administrators only). The user is named in the path.
func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	writeJSON(w, u)
}

// handleUpdateUser replaces the editable subset of a user's record (system
// administrators only). The whole subset is replaced, so every editable field
// must be supplied; identity fields (username, domain, maildir) are immutable.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status      int    `json:"status"`
		Lang        string `json:"lang"`
		Timezone    string `json:"timezone"`
		DisplayType int    `json:"displayType"`
		Homeserver  int    `json:"homeserver"`
		POP3IMAP    bool   `json:"pop3_imap"`
		SMTP        bool   `json:"smtp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	found, err := s.dir.UpdateUser(r.PathValue("email"), directory.UserUpdate{
		Status:      req.Status,
		Lang:        req.Lang,
		Timezone:    req.Timezone,
		DisplayType: req.DisplayType,
		Homeserver:  req.Homeserver,
		POP3IMAP:    req.POP3IMAP,
		SMTP:        req.SMTP,
	})
	if err != nil {
		http.Error(w, "could not update user: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !found {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteUser removes a user (system administrators only). The maildir is
// removed from disk only when the deleteFiles query parameter is "true".
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	deleteFiles := r.URL.Query().Get("deleteFiles") == "true"
	found, err := s.dir.DeleteUser(r.PathValue("email"), deleteFiles)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListAltnames returns a user's alternative login names (system
// administrators only).
func (s *Server) handleListAltnames(w http.ResponseWriter, r *http.Request) {
	names, err := s.dir.ListAltnames(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, names)
}

// handleSetAltnames replaces a user's alternative login names (system
// administrators only). A name already taken by another account is rejected.
func (s *Server) handleSetAltnames(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Altnames []string `json:"altnames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	found, err := s.dir.SetAltnames(r.PathValue("email"), req.Altnames)
	if err != nil {
		http.Error(w, "could not set alternative names: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !found {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
