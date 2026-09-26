package webmail2api

import (
	"encoding/json"
	"net/http"

	"hermex/internal/objectstore"
)

// sentCopyJSON is the SPA's sent-copy shape: whether a message another account sends
// as this mailbox, and whether one it sends on this mailbox's behalf, also lands in
// this mailbox's Sent Items, and whether that copy is then the only one (the sender
// keeps none).
type sentCopyJSON struct {
	ForSendAs       bool `json:"forSendAs"`
	ForSendOnBehalf bool `json:"forSendOnBehalf"`
	Exclusive       bool `json:"exclusive"`
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
	writeJSON(w, http.StatusOK, sentCopyJSON(cfg))
}

// handlePutSentCopy replaces every flag at once. The client sends the whole object, so
// a change to one never drops the others.
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
	if err := st.SetSentCopyConfig(objectstore.SentCopyConfig(in)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save"})
		return
	}
	writeJSON(w, http.StatusOK, in)
}
