package webmail2api

import (
	"encoding/json"
	"net/http"

	"hermex/internal/objectstore"
)

// sentCopyJSON is the SPA's sent-copy shape: whether a message another account sends
// as this mailbox, and whether one it sends on this mailbox's behalf, also lands in
// this mailbox's Sent Items. The sender keeps their own copy either way.
type sentCopyJSON struct {
	ForSendAs       bool `json:"forSendAs"`
	ForSendOnBehalf bool `json:"forSendOnBehalf"`
}

func (s *Server) handleGetSentCopy(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	cfg, err := st.GetSentCopyConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read"})
		return
	}
	writeJSON(w, http.StatusOK, sentCopyJSON{ForSendAs: cfg.ForSendAs, ForSendOnBehalf: cfg.ForSendOnBehalf})
}

// handlePutSentCopy replaces both flags at once. The client sends the whole object, so
// a change to one never drops the other.
func (s *Server) handlePutSentCopy(w http.ResponseWriter, r *http.Request) {
	var in sentCopyJSON
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	cfg := objectstore.SentCopyConfig{ForSendAs: in.ForSendAs, ForSendOnBehalf: in.ForSendOnBehalf}
	if err := st.SetSentCopyConfig(cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save"})
		return
	}
	writeJSON(w, http.StatusOK, in)
}
