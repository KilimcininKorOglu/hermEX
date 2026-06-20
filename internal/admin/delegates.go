package admin

import (
	"net/http"
	"strings"
)

// handleUIUserDelegates saves the user's public-delegate list from the detail form
// (one address per line, whitespace-separated like the aliases form) and returns
// the refreshed status panel. This is the same per-mailbox list NSPI serves and a
// user edits from Outlook, so the admin console and the client manage one source
// of truth.
func (s *Server) handleUIUserDelegates(w http.ResponseWriter, r *http.Request) {
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
		if err := s.store.SetDelegates(u.Maildir, strings.Fields(r.PostFormValue("delegates"))); err != nil {
			data["Error"] = "Could not save delegates: " + err.Error()
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
}
