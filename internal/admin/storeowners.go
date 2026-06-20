package admin

import (
	"encoding/json"
	"net/http"
	"strings"
)

// handleGetUserStoreOwners returns the user's additional store-owner list — the users
// granted full read-write access to the whole mailbox (system administrators only).
func (s *Server) handleGetUserStoreOwners(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	list, err := s.store.GetStoreOwners(maildir)
	if err != nil {
		http.Error(w, "could not read store owners", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []string{}
	}
	writeJSON(w, map[string]any{"data": list})
}

// handleSetUserStoreOwners replaces the user's additional store-owner list (system
// administrators only). Every owner must name a real user; an unknown address is
// refused so a dead grant is never stored.
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
	list, bad, err := s.canonicalGrantees(in)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if bad != "" {
		http.Error(w, "no such user: "+bad, http.StatusNotFound)
		return
	}
	if err := s.store.SetStoreOwners(maildir, list); err != nil {
		http.Error(w, "could not set store owners: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUIUserStoreOwners saves the user's additional store-owner list from the detail
// form (one address per line) and returns the refreshed status panel. Each owner must
// name a real user; an unknown address is reported rather than stored as a dead grant.
func (s *Server) handleUIUserStoreOwners(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = "Server error."
	case !ok:
		data["Error"] = "No such user."
	default:
		list, bad, gErr := s.canonicalGrantees(strings.Fields(r.PostFormValue("storeowners")))
		switch {
		case gErr != nil:
			data["Error"] = "Server error."
		case bad != "":
			data["Error"] = "No such user: " + bad + "."
		default:
			if err := s.store.SetStoreOwners(u.Maildir, list); err != nil {
				data["Error"] = "Could not save store owners: " + err.Error()
			} else {
				data["Saved"] = true
			}
		}
	}
	s.render(w, "user-status", data)
}
