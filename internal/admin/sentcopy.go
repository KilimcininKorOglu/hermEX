package admin

import (
	"encoding/json"
	"net/http"

	"hermex/internal/objectstore"
)

// sentCopyPayload is the JSON shape of a mailbox's sent-copy configuration: whether a
// message sent as this mailbox, and whether one sent on its behalf, also lands in its
// own Sent Items. Without it the only copy is in the sender's own Sent Items, so
// nobody else with access to a shared mailbox sees what went out in its name.
type sentCopyPayload struct {
	ForSendAs       bool `json:"forSendAs"`
	ForSendOnBehalf bool `json:"forSendOnBehalf"`
}

// handleGetUserSentCopy returns a user's sent-copy configuration.
func (s *Server) handleGetUserSentCopy(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	cfg, err := s.store.GetSentCopyConfig(maildir)
	if err != nil {
		http.Error(w, "could not read sent-copy config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, sentCopyPayload{ForSendAs: cfg.ForSendAs, ForSendOnBehalf: cfg.ForSendOnBehalf})
}

// handleSetUserSentCopy replaces a user's sent-copy configuration.
func (s *Server) handleSetUserSentCopy(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	var in sentCopyPayload
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	cfg := objectstore.SentCopyConfig{ForSendAs: in.ForSendAs, ForSendOnBehalf: in.ForSendOnBehalf}
	if err := s.store.SetSentCopyConfig(maildir, cfg); err != nil {
		s.fail(w, "could not set sent-copy config", err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUIUserSentCopy saves the two sent-copy checkboxes from the detail form and
// returns the refreshed status panel.
func (s *Server) handleUIUserSentCopy(w http.ResponseWriter, r *http.Request) {
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
		cfg := objectstore.SentCopyConfig{
			ForSendAs:       r.PostFormValue("copysendas") != "",
			ForSendOnBehalf: r.PostFormValue("copysendonbehalf") != "",
		}
		if err := s.store.SetSentCopyConfig(u.Maildir, cfg); err != nil {
			data["Error"] = s.notice("Could not save sent-copy settings.", err)
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
}
