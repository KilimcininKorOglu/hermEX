package webmail2api

import (
	"fmt"
	"net/http"
	"time"
)

// handleEvents serves the SPA's Server-Sent Events stream. It opens a valid
// text/event-stream and heartbeats to keep the connection alive. Real change
// notifications (new_mail/expunge/flags_changed/folder_update) require a
// cross-daemon notification channel from the MTA/store and are not pushed yet;
// the SPA refetches on navigation in the meantime.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
