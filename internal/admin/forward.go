package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"hermex/internal/directory"
)

// forwardPayload is the JSON shape of a user's mail-forward directive: the forward
// type (0=CC, 1=Redirect) and the destination address. An empty destination means no
// forward is set (GET) or clears it (PUT).
type forwardPayload struct {
	ForwardType int    `json:"forwardType"`
	Destination string `json:"destination"`
}

// handleGetUserForward returns a user's mail-forward directive (system administrators
// only); an unset forward is an empty payload.
func (s *Server) handleGetUserForward(w http.ResponseWriter, r *http.Request) {
	fi, ok, err := s.dir.GetForward(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		writeJSON(w, forwardPayload{})
		return
	}
	writeJSON(w, forwardPayload{ForwardType: fi.Type, Destination: fi.Destination})
}

// handleSetUserForward sets or clears a user's mail-forward directive (system
// administrators only). An empty destination clears the forward.
func (s *Server) handleSetUserForward(w http.ResponseWriter, r *http.Request) {
	var in forwardPayload
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	found, err := s.dir.SetForward(r.PathValue("email"), in.ForwardType, in.Destination)
	if err != nil {
		http.Error(w, "could not set forward: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !found {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// forwardView is the forward section's template model: the configured type as a
// string for the <select> ("" / "cc" / "redirect") and the destination address.
type forwardView struct {
	Type        string
	Destination string
}

// forwardViewOf builds the forward section model from a directory lookup; an unset or
// unreadable forward renders as none.
func (s *Server) forwardViewOf(username string) forwardView {
	fi, ok, err := s.dir.GetForward(username)
	if err != nil || !ok {
		return forwardView{}
	}
	if fi.Type == directory.ForwardRedirect {
		return forwardView{Type: "redirect", Destination: fi.Destination}
	}
	return forwardView{Type: "cc", Destination: fi.Destination}
}

// handleUIUserForward saves the user's forward directive from the detail form and
// returns the refreshed status panel; the "—" type (or an empty destination) clears
// it. A directory error is reported in the panel rather than failing the request.
func (s *Server) handleUIUserForward(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	ftype, destination := forwardFromForm(r)
	found, err := s.dir.SetForward(r.PathValue("email"), ftype, destination)
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = "Could not save forward: " + err.Error()
	case !found:
		data["Error"] = "No such user."
	default:
		data["Saved"] = true
	}
	s.render(w, "user-status", data)
}

// forwardFromForm reads the forward form: the type select ("" / "cc" / "redirect")
// and destination. The none type clears the forward by passing an empty destination,
// which the directory treats as a delete.
func forwardFromForm(r *http.Request) (forwardType int, destination string) {
	destination = strings.TrimSpace(r.PostFormValue("destination"))
	switch r.PostFormValue("forwardtype") {
	case "redirect":
		return directory.ForwardRedirect, destination
	case "cc":
		return directory.ForwardCC, destination
	default:
		return directory.ForwardCC, "" // none: clear the directive
	}
}
