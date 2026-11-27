package webmail2api

import (
	"errors"
	"net/http"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// groupDir is the narrow directory view the webmail group-management surface needs:
// the lists a user owns and read/write access to a list's members. *SQLDirectory
// satisfies it.
type groupDir interface {
	ListMListsOwnedBy(owner string) ([]directory.MListInfo, error)
	ListMembers(listname string) ([]string, error)
	SetMembers(listname string, members []string) (bool, error)
}

// ownedInScope returns the lists email owns that it can still reach. Ownership is a
// grant over the list's members and stays inside the list's organization, so a
// stored owner outside it (a row written before the owner rule, or an account that
// has since moved) manages nothing. A scope that cannot be read is an error, never a
// shorter list.
func ownedInScope(gd groupDir, accounts directory.Accounts, email string) ([]directory.MListInfo, error) {
	owned, err := gd.ListMListsOwnedBy(email)
	if err != nil {
		return nil, err
	}
	out := owned[:0:0]
	for _, l := range owned {
		reach, err := directory.MayReach(accounts, l.Listname, email)
		if err != nil {
			return nil, err
		}
		if reach {
			out = append(out, l)
		}
	}
	return out, nil
}

// ownsList reports whether email owns the list at addr (the managedBy owner). Every
// member operation is gated on it so a caller can only manage lists they own, never
// another owner's list (Broken Access Control / IDOR).
func ownsList(gd groupDir, accounts directory.Accounts, email, addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return false
	}
	owned, err := ownedInScope(gd, accounts, email)
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
	owned, err := ownedInScope(gd, s.accounts, c.Email)
	if err != nil {
		logError("groups-list", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list your groups"})
		return
	}
	out := make([]map[string]any, 0, len(owned))
	for _, l := range owned {
		out = append(out, map[string]any{"address": l.Listname, "ldapMastered": l.LDAPMastered})
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
	if !ownsList(gd, s.accounts, c.Email, addr) {
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
	if !ownsList(gd, s.accounts, c.Email, req.Address) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you do not own this group"})
		return
	}
	existed, err := gd.SetMembers(req.Address, req.Members)
	if err != nil {
		if errors.Is(err, directory.ErrLDAPMastered) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "this group is managed by directory sync and cannot be edited here"})
			return
		}
		if errors.Is(err, directory.ErrUnroutableAddress) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "every member must be a single email address"})
			return
		}
		logError("groups-set-members", err, logging.Fields{"list": req.Address})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save members"})
		return
	}
	if !existed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such group"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
