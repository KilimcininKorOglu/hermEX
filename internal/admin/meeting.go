package admin

import (
	"encoding/json"
	"net/http"

	"hermex/internal/objectstore"
)

// meetingPayload is the JSON shape of a mailbox's meeting-request handling
// configuration: accept conflict-free requests (the master), decline recurring or
// conflicting ones, whether answering a request also files it away, and whether
// delivered cancellations are left for the user instead of applied. Every field
// defaults to false, so a client that omits one asks for the default behaviour.
type meetingPayload struct {
	AutoAccept                    bool `json:"autoAccept"`
	DeclineRecurring              bool `json:"declineRecurring"`
	DeclineConflict               bool `json:"declineConflict"`
	RemoveRequestOnResponse       bool `json:"removeRequestOnResponse"`
	LeaveCancellationsUnprocessed bool `json:"leaveCancellationsUnprocessed"`
}

// handleGetUserMeeting returns a user's automatic meeting-processing settings (system
// administrators only).
func (s *Server) handleGetUserMeeting(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	cfg, err := s.store.GetMeetingConfig(maildir)
	if err != nil {
		http.Error(w, "could not read meeting config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, meetingPayload(cfg))
}

// handleSetUserMeeting replaces a user's automatic meeting-processing settings (system
// administrators only).
func (s *Server) handleSetUserMeeting(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	var in meetingPayload
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.store.SetMeetingConfig(maildir, objectstore.MeetingConfig(in)); err != nil {
		s.fail(w, "could not set meeting config", err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUIUserMeeting saves the meeting-handling checkboxes from the detail form
// and returns the refreshed status panel.
func (s *Server) handleUIUserMeeting(w http.ResponseWriter, r *http.Request) {
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
		cfg := objectstore.MeetingConfig{
			AutoAccept:              r.PostFormValue("autoaccept") != "",
			DeclineRecurring:        r.PostFormValue("declinerecurring") != "",
			DeclineConflict:         r.PostFormValue("declineconflict") != "",
			RemoveRequestOnResponse: r.PostFormValue("removerequest") != "",
			// The form asks the positive question, so an unchecked box is the exception.
			LeaveCancellationsUnprocessed: r.PostFormValue("processcancellations") == "",
		}
		if err := s.store.SetMeetingConfig(u.Maildir, cfg); err != nil {
			data["Error"] = s.notice("Could not save meeting settings.", err)
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
}
