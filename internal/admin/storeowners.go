package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"hermex/internal/directory"
)

// handleGetUserStoreOwners returns the user's additional store-owner list, the users
// granted full read-write access to the whole mailbox. Gated by requireUserScope.
func (s *Server) handleGetUserStoreOwners(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	list, err := s.store.GetStoreOwners(maildir)
	if err != nil {
		s.fail(w, "could not read store owners", err, http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []string{}
	}
	writeJSON(w, map[string]any{"data": list})
}

// handleSetUserStoreOwners replaces the user's additional store-owner list. Gated
// by requireUserScope on the target, and separately by addressScopeError on each
// owner: the grant hands full read-write access to a second, independent account,
// which the target's own domain says nothing about. Every owner must name a real
// user; an unknown address is refused so a dead grant is never stored.
func (s *Server) handleSetUserStoreOwners(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	var in []string
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if bad, ok := s.addressScopeError(s.adminPerms(claimsOf(r).UserID), in); !ok {
		http.Error(w, scopeRefusal("store owner", bad), http.StatusForbidden)
		return
	}
	list, bad, err := s.canonicalGrantees(in)
	if err != nil {
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return
	}
	if bad != "" {
		http.Error(w, "no such user: "+bad, http.StatusNotFound)
		return
	}
	if s.refuseOutsideOrg(w, r.PathValue("email"), list) {
		return
	}
	if err := s.store.SetStoreOwners(maildir, list); err != nil {
		s.fail(w, "could not set store owners", err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUIUserStoreOwners saves the user's additional store-owner list from the detail
// form (one address per line) and returns the refreshed status panel. Each owner must
// name a real user; an unknown address is reported rather than stored as a dead grant.
func (s *Server) handleUIUserStoreOwners(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiAuthorized(w, r)
	if !ok {
		return
	}
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = s.notice(userUnread, err)
	case !ok:
		data["Error"] = "userDetail.noSuchUser"
	default:
		if refusal := s.saveStoreOwnersForm(cl.UserID, u, strings.Fields(r.PostFormValue("storeowners"))); refusal != "" {
			data["Error"] = refusal
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, r, "user-status", data)
}

// saveStoreOwnersForm checks and stores a store-owner list from the detail form,
// returning the notice to show, or "" when the list was stored.
func (s *Server) saveStoreOwnersForm(adminID int64, u directory.UserDetail, grantees []string) string {
	list, bad, gErr := s.canonicalGrantees(grantees)
	outOfScope, inScope := s.addressScopeError(s.adminPerms(adminID), grantees)
	switch {
	case !inScope:
		return scopeRefusalMsg("scope.storeOwner", outOfScope)
	case gErr != nil:
		return s.notice(granteesUnread, gErr)
	case bad != "":
		return msg("userDetail.noSuchUserNamed", bad)
	}
	if refusal := s.orgRefusal(u.Username, list); refusal != "" {
		return refusal
	}
	if err := s.store.SetStoreOwners(u.Maildir, list); err != nil {
		return s.notice("userDetail.saveStoreOwnersFailed", err)
	}
	return ""
}
