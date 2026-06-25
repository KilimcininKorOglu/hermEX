package webmail2api

import (
	"fmt"
	"net/http"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// eventsPollInterval is how often an open Server-Sent Events stream samples the
// caller's inbox for changes. Each tick either pushes a change event or a heartbeat
// comment, so it doubles as the keep-alive.
const eventsPollInterval = 10 * time.Second

// inboxDelta classifies a change in the inbox counts into the SSE event the SPA
// listens for, or "" when nothing actionable changed. The first observation
// (prevTotal < 0) only records a baseline so a freshly opened stream never fires for
// mail that was already there. A higher total is new mail; any other shift (a delete
// or a read/unread change) is a generic folder update the SPA refetches on.
func inboxDelta(prevTotal, prevUnread, total, unread int) string {
	switch {
	case prevTotal < 0:
		return ""
	case total > prevTotal:
		return "new_mail"
	case total != prevTotal || unread != prevUnread:
		return "folder_update"
	default:
		return ""
	}
}

// handleEvents serves the SPA's Server-Sent Events stream. It opens a valid
// text/event-stream and, every eventsPollInterval, samples the caller's inbox and
// pushes a new_mail or folder_update event when the counts move, so open tabs update
// live; ticks with no change send a heartbeat comment to hold the connection open.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
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

	lastTotal, lastUnread := -1, -1
	ticker := time.NewTicker(eventsPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			event := ""
			if c.Mailbox != "" {
				// Open-count-close per tick (as the push poller does) so the stream
				// never holds the mailbox open between samples.
				if st, err := objectstore.Open(c.Mailbox); err == nil {
					if total, unread, err := st.CountMessages(mapi.PrivateFIDInbox); err == nil {
						event = inboxDelta(lastTotal, lastUnread, total, unread)
						lastTotal, lastUnread = total, unread
					}
					st.Close()
				}
			}
			var err error
			if event != "" {
				_, err = fmt.Fprintf(w, "event: %s\ndata: {}\n\n", event)
			} else {
				_, err = fmt.Fprint(w, ": ping\n\n")
			}
			if err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
