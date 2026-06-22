package admin

import "net/http"

// defaultLogRetentionDaysDisplay is the value shown until one is saved (or the admin
// daemon seeds it from config on first run): zero, meaning keep logs forever. The admin
// daemon prunes the central log store to the saved window.
const defaultLogRetentionDaysDisplay = 0

// fillLogRetention sets the central-log retention window (in days) on a page-data map,
// using the stored value or the keep-forever default when none has been saved. Shared by
// the Settings page so its Retention tab can render the log-retention panel.
func (s *Server) fillLogRetention(data map[string]any) {
	days := defaultLogRetentionDaysDisplay
	if d, found, err := s.dir.GetLogRetentionDays(); err == nil && found {
		days = d
	}
	data["LogRetentionDays"] = days
}

// logRetentionPanelData builds the model the log-retention panel renders: the stored
// window (or the keep-forever default) plus the notice and CSRF token its htmx form needs.
func (s *Server) logRetentionPanelData(r *http.Request, notice string) map[string]any {
	data := map[string]any{"Notice": notice, "CSRF": csrfCookieValue(r)}
	s.fillLogRetention(data)
	return data
}

// handleUISaveLogRetention persists the central-log retention window (in whole days).
// The admin daemon prunes the log store to match within about a minute, no restart. A
// value of zero means keep logs forever — pruning is disabled — which is allowed and is
// the safe default; formInt already maps a blank or negative entry to zero.
func (s *Server) handleUISaveLogRetention(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	days := formInt(r, "log_retention_days")
	if err := s.dir.SetLogRetentionDays(days); err != nil {
		s.render(w, "log-retention-panel", s.logRetentionPanelData(r, "Could not save the retention setting: "+err.Error()))
		return
	}
	msg := "Log retention saved — the admin prunes the log store to match within a minute, no restart."
	if days == 0 {
		msg = "Log retention set to keep forever — pruning is disabled."
	}
	s.render(w, "log-retention-panel", s.logRetentionPanelData(r, msg))
}
