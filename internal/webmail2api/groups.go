package webmail2api

import (
	"net/http"
	"strings"

	"hermex/internal/directory"
)

// groupDir is the narrow directory view the webmail group-management surface needs:
// the lists a user owns and read/write access to a list's members. *SQLDirectory
// satisfies it.
type groupDir interface {
	ListMListsOwnedBy(owner string) ([]directory.MListInfo, error)
	ListMembers(listname string) ([]string, error)
	SetMembers(listname string, members []string) (bool, error)
}

// ownsList reports whether email owns the list at addr (the managedBy owner). Every
// member operation is gated on it so a caller can only manage lists they own, never
// another owner's list (Broken Access Control / IDOR).
func ownsList(gd groupDir, email, addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return false
	}
	owned, err := gd.ListMListsOwnedBy(email)
	if err != nil {
		return false
	}
	for _, l := range owned {
		if strings.ToLower(l.Listname) == addr {
			return true
		}
	}
	return false
}

// handleGroups lists the distribution lists the caller owns.
func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	gd, ok := s.auth.(groupDir)
	if !ok {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	owned, err := gd.ListMListsOwnedBy(c.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list your groups"})
		return
	}
	out := make([]map[string]any, 0, len(owned))
	for _, l := range owned {
		out = append(out, map[string]any{"address": l.Listname})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGroupMembers returns the members of a list the caller owns.
func (s *Server) handleGroupMembers(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	gd, ok := s.auth.(groupDir)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "group management is not available"})
		return
	}
	addr := r.URL.Query().Get("address")
	if !ownsList(gd, c.Email, addr) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you do not own this group"})
		return
	}
	members, err := gd.ListMembers(addr)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load members"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"address": addr, "members": members})
}

// handleGroupSetMembers replaces the members of a list the caller owns. The SPA may
// fill the member list by hand or by importing a CSV; either way the full set is
// posted here.
func (s *Server) handleGroupSetMembers(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	gd, ok := s.auth.(groupDir)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "group management is not available"})
		return
	}
	var req struct {
		Address string   `json:"address"`
		Members []string `json:"members"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if !ownsList(gd, c.Email, req.Address) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you do not own this group"})
		return
	}
	if _, err := gd.SetMembers(req.Address, req.Members); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save members"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
