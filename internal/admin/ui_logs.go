package admin

import (
	"context"
	"net/http"
	"time"
)

// handleUILogs renders the central log viewer (system administrators only),
// listing the most recent events with an optional subsystem filter.
func (s *Server) handleUILogs(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	data := map[string]any{"Nav": "logs", "CSRF": csrfCookieValue(r)}
	if s.logs == nil {
		data["Disabled"] = true
		s.render(w, "logs.html", data)
		return
	}
	sub := r.URL.Query().Get("subsystem")
	data["Subsystem"] = sub
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	entries, err := s.logs.Recent(ctx, sub, 200)
	if err != nil {
		data["Error"] = "Could not query the log store."
	} else {
		data["Entries"] = entries
	}
	s.render(w, "logs.html", data)
}
