package admin

import (
	"encoding/json"
	"net/http"
	"strings"
)

// canonicalGrantees lowercases each grantee address and confirms it names a real
// user, returning the canonical list. It reports the first address that resolves to
// no user (bad) so the caller can refuse the whole set rather than store a dead grant
// the MTA's send-as check could never honor. Blank entries are dropped. Storing the
// canonical primary is enough because the MTA matches a grant against the grantee's
// full identity set, so any of their addresses logs in to it.
func (s *Server) canonicalGrantees(list []string) (canonical []string, bad string, err error) {
	for _, raw := range list {
		g := strings.ToLower(strings.TrimSpace(raw))
		if g == "" {
			continue
		}
		_, ok, e := s.dir.GetUser(g)
		if e != nil {
			return nil, "", e
		}
		if !ok {
			return nil, g, nil
		}
		canonical = append(canonical, g)
	}
	return canonical, "", nil
}

// handleGetUserSendAs returns the user's send-as list — the addresses permitted to
// send mail as this user (system administrators only).
func (s *Server) handleGetUserSendAs(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	list, err := s.store.GetSendAs(maildir)
	if err != nil {
		http.Error(w, "could not read send-as", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []string{}
	}
	writeJSON(w, map[string]any{"data": list})
}

// handleSetUserSendAs replaces the user's send-as list (system administrators only).
// Every grantee must name a real user; an unknown address is refused so a dead grant
// is never stored.
func (s *Server) handleSetUserSendAs(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.SetSendAs(maildir, list); err != nil {
		http.Error(w, "could not set send-as: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUIUserSendAs saves the user's send-as list from the detail form (one address
// per line) and returns the refreshed status panel. Each grantee must name a real
// user; an unknown address is reported rather than stored as a dead grant.
func (s *Server) handleUIUserSendAs(w http.ResponseWriter, r *http.Request) {
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
		list, bad, gErr := s.canonicalGrantees(strings.Fields(r.PostFormValue("sendas")))
		switch {
		case gErr != nil:
			data["Error"] = "Server error."
		case bad != "":
			data["Error"] = "No such user: " + bad + "."
		default:
			if err := s.store.SetSendAs(u.Maildir, list); err != nil {
				data["Error"] = "Could not save send-as: " + err.Error()
			} else {
				data["Saved"] = true
			}
		}
	}
	s.render(w, "user-status", data)
}
