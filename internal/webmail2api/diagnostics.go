package webmail2api

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// diagnosticJSON is the SPA's DiagnosticEntry shape.
type diagnosticJSON struct {
	ID        string `json:"id"`
	Severity  string `json:"severity"`
	Category  string `json:"category"`
	Message   string `json:"message"`
	Mailbox   string `json:"mailbox,omitempty"`
	Timestamp string `json:"timestamp"`
	Retryable bool   `json:"retryable"`
	NextStep  string `json:"nextStep,omitempty"`
}

// handleDiagnostics reports the caller's own outbound delivery problems: messages
// still stuck in the relay queue after a failed attempt, surfaced to the compose
// view so a user can see why a recent send has not gone out. It is scoped to the
// caller's own sends - a request naming another mailbox returns nothing rather than
// exposing that mailbox's queue.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	mailbox := strings.TrimSpace(r.URL.Query().Get("mailbox"))
	if mailbox == "" {
		mailbox = c.Email
	}
	out := make([]diagnosticJSON, 0)
	if strings.EqualFold(mailbox, c.Email) && s.spool != nil {
		if entries, err := s.spool.List(); err == nil {
			for _, e := range entries {
				if e.LastError == "" || !strings.EqualFold(e.From, c.Email) {
					continue
				}
				out = append(out, diagnosticJSON{
					ID:        fmt.Sprintf("delivery-%d-%d", e.MessageID, e.RecipientID),
					Severity:  "warning",
					Category:  "delivery",
					Message:   fmt.Sprintf("Delivery to %s is failing: %s", e.Recipient, e.LastError),
					Mailbox:   c.Email,
					Timestamp: e.EnqueuedAt.UTC().Format(time.RFC3339),
					Retryable: true,
					NextStep:  "The server will keep retrying (next attempt " + e.NextAttempt.UTC().Format(time.RFC3339) + ")",
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"errors": out})
}
