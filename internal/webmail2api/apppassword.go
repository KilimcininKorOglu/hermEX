package webmail2api

import (
	"errors"
	"net/http"
	"strconv"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/serve"
)

// appPasswordJSON is one credential as the SPA lists it. The secret is absent on
// purpose: it exists in the response that mints it and nowhere else.
type appPasswordJSON struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt int64  `json:"lastUsedAt"`
}

// appPasswords returns the directory's app-password capability.
func (s *Server) appPasswords() (directory.AppPasswordStore, bool) {
	st, ok := s.auth.(directory.AppPasswordStore)
	return st, ok
}

func (s *Server) handleAppPasswordList(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	store, ok := s.appPasswords()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"appPasswords": []appPasswordJSON{}})
		return
	}
	list, err := store.ListAppPasswords(c.Email)
	if err != nil {
		logError("app-password-list", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	out := make([]appPasswordJSON, 0, len(list))
	for _, p := range list {
		out = append(out, appPasswordJSON{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, LastUsedAt: p.LastUsedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"appPasswords": out})
}

// handleAppPasswordCreate mints a credential for one mail program. The account
// password is asked for again, because a credential is a way into the mailbox
// that survives a password change, which is what someone holding a stolen
// session would want most.
func (s *Server) handleAppPasswordCreate(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	store, ok := s.appPasswords()
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "app passwords are unavailable"})
		return
	}
	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	addr := serve.ClientAddr(r)
	if !s.limiter.Allowed(addr, c.Email) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed attempts, try again later"})
		return
	}
	if _, ok := s.auth.Authenticate(c.Email, req.Password); !ok {
		s.limiter.Fail(addr, c.Email)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	s.limiter.Succeed(addr, c.Email)
	secret, err := store.CreateAppPassword(c.Email, req.Name)
	if errors.Is(err, directory.ErrTooManyAppPasswords) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "too many app passwords; revoke one first"})
		return
	}
	if err != nil {
		logError("app-password-create", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret})
}

func (s *Server) handleAppPasswordDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	store, ok := s.appPasswords()
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "app passwords are unavailable"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	// The caller's own address scopes the delete in SQL, so naming another
	// account's credential id removes nothing.
	removed, err := store.DeleteAppPassword(c.Email, id)
	if err != nil {
		logError("app-password-delete", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if !removed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}
