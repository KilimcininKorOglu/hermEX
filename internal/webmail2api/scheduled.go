package webmail2api

import (
	"net/http"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mime"
)

// handleScheduled lists the Outbox's deferred ("send later") messages so the SPA
// can show them. Each carries its absolute send time from PR_DEFERRED_SEND_TIME.
func (s *Server) handleScheduled(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	msgs, _ := st.ListMessages(mapi.PrivateFIDOutbox)
	items := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		sendAt := ""
		if props, err := st.GetMessageProperties(m.ID, mapi.PrDeferredSendTime); err == nil {
			if v, ok := props.Get(mapi.PrDeferredSendTime); ok {
				if nt, ok := v.(uint64); ok {
					sendAt = mapi.NTTimeToUnix(nt).UTC().Format(time.RFC3339)
				}
			}
		}
		to := []string{}
		if raw, err := st.GetMessageRaw(mapi.PrivateFIDOutbox, m.UID); err == nil {
			if env, err := mime.ParseEnvelope(raw); err == nil {
				to = addrEmails(env.To)
			}
		}
		items = append(items, map[string]any{
			"id":      messageID("outbox", m.UID),
			"to":      to,
			"subject": m.Subject,
			"sendAt":  sendAt,
			"status":  "pending",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"scheduled": items})
}

// handleCancelScheduled cancels a scheduled send by moving it back to Drafts, so
// the composed message is kept rather than lost.
func (s *Server) handleCancelScheduled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	folder, uid, ok := parseMessageID(body.ID)
	if !ok || folder != "outbox" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	if _, err := st.MoveMessage(int64(mapi.PrivateFIDOutbox), uid, int64(mapi.PrivateFIDDraft)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not cancel the scheduled send"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
