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

// A mailbox carries two send-grant lists, and they are the same shape with the same
// scope rules: send-as names who may put this mailbox in From with nothing else added,
// send-on-behalf names who may do the same while the message also names them in
// Sender. The read, the write and the detail-form save are therefore written once and
// addressed by which list they carry, so the two can never drift apart.

// handleGetUserSendAs returns the user's send-as list, the addresses permitted to
// send mail as this user. Gated by requireUserScope.
func (s *Server) handleGetUserSendAs(w http.ResponseWriter, r *http.Request) {
	s.serveGrantList(w, r, "send-as", s.store.GetSendAs)
}

// handleSetUserSendAs replaces the user's send-as list.
func (s *Server) handleSetUserSendAs(w http.ResponseWriter, r *http.Request) {
	s.saveGrantList(w, r, "send-as", s.store.SetSendAs)
}

// handleUIUserSendAs saves the user's send-as list from the detail form (one address
// per line) and returns the refreshed status panel.
func (s *Server) handleUIUserSendAs(w http.ResponseWriter, r *http.Request) {
	s.saveGrantListForm(w, r, "send-as", "sendas", s.store.SetSendAs)
}

// handleGetUserSendOnBehalf returns the user's send-on-behalf list, the addresses
// permitted to send mail on this user's behalf. Gated by requireUserScope.
func (s *Server) handleGetUserSendOnBehalf(w http.ResponseWriter, r *http.Request) {
	s.serveGrantList(w, r, "send-on-behalf", s.store.GetSendOnBehalf)
}

// handleSetUserSendOnBehalf replaces the user's send-on-behalf list.
func (s *Server) handleSetUserSendOnBehalf(w http.ResponseWriter, r *http.Request) {
	s.saveGrantList(w, r, "send-on-behalf", s.store.SetSendOnBehalf)
}

// handleUIUserSendOnBehalf saves the user's send-on-behalf list from the detail form
// (one address per line) and returns the refreshed status panel.
func (s *Server) handleUIUserSendOnBehalf(w http.ResponseWriter, r *http.Request) {
	s.saveGrantListForm(w, r, "send-on-behalf", "sendonbehalf", s.store.SetSendOnBehalf)
}

// serveGrantList writes one of a mailbox's send-grant lists as a JSON address array.
func (s *Server) serveGrantList(w http.ResponseWriter, r *http.Request, kind string, get func(string) ([]string, error)) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	list, err := get(maildir)
	if err != nil {
		http.Error(w, "could not read "+kind, http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []string{}
	}
	writeJSON(w, map[string]any{"data": list})
}

// saveGrantList replaces one of a mailbox's send-grant lists. Gated by
// requireUserScope on the target, and separately by addressScopeError on each grantee:
// the grant is handed to a second, independent account, which the target's domain does
// not constrain. Every grantee must name a real user; an unknown address is refused so
// a dead grant is never stored.
func (s *Server) saveGrantList(w http.ResponseWriter, r *http.Request, kind string, set func(string, []string) error) {
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
		http.Error(w, scopeRefusal(kind+" grantee", bad), http.StatusForbidden)
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
	if err := set(maildir, list); err != nil {
		s.fail(w, "could not set "+kind, err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// saveGrantListForm saves one grant list from the detail form, where the addresses
// arrive one per line, and returns the refreshed status panel.
func (s *Server) saveGrantListForm(w http.ResponseWriter, r *http.Request, kind, field string, set func(string, []string) error) {
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
		grantees := strings.Fields(r.PostFormValue(field))
		if msg := s.storeGrantList(r, u.Maildir, kind, grantees, set); msg != "" {
			data["Error"] = msg
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
}

// storeGrantList canonicalizes the grantees, checks the caller may grant to each of
// them, and stores the list. It returns the message to show the operator, or "" when
// the list was stored.
func (s *Server) storeGrantList(r *http.Request, maildir, kind string, grantees []string, set func(string, []string) error) string {
	list, bad, gErr := s.canonicalGrantees(grantees)
	outOfScope, inScope := s.addressScopeError(s.adminPerms(claimsOf(r).UserID), grantees)
	switch {
	case !inScope:
		return scopeRefusal(kind+" grantee", outOfScope)
	case gErr != nil:
		return "Server error."
	case bad != "":
		return "No such user: " + bad + "."
	}
	if err := set(maildir, list); err != nil {
		return s.notice("Could not save "+kind+".", err)
	}
	return ""
}
