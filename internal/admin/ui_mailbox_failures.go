package admin

import (
	"context"
	"net/http"
	"time"
)

// mailboxUnusableEvent is the event the MTA records when a recipient's mailbox
// database is condemned rather than merely unavailable.
const mailboxUnusableEvent = "delivery.mailbox_unusable"

// handleUIMailboxFailures lists the mailboxes whose database refused a delivery
// permanently (system administrators only). Delivery defers such a message, so
// the sender keeps retrying and nothing bounces; without this page the only
// trace is a delivery.fail line that reads like any transient store failure.
func (s *Server) handleUIMailboxFailures(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	data := map[string]any{"Nav": "mailboxfailures", "CSRF": csrfCookieValue(r)}
	if s.logs == nil {
		data["Disabled"] = true
		s.render(w, "mailbox_failures.html", data)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	entries, err := s.logs.RecentByEvent(ctx, mailboxUnusableEvent, 200)
	if err != nil {
		data["Error"] = "Could not query the log store."
	} else {
		data["Entries"] = entries
	}
	s.render(w, "mailbox_failures.html", data)
}
