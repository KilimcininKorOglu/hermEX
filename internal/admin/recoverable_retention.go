package admin

import (
	"net/http"

	"hermex/internal/directory"
)

// fillRecoverableRetention sets the Recoverable Items retention window (in days) on a
// page-data map, using the stored value or the Exchange-matching default when none has
// been saved. Shared by the Settings page so its Retention tab can render the panel.
func (s *Server) fillRecoverableRetention(data map[string]any) {
	days := directory.DefaultRecoverableRetentionDays
	if rs, found, err := s.dir.GetRecoverableSettings(); err == nil && found {
		days = rs.RetentionDays
	}
	data["RecoverableRetentionDays"] = days
}

// recoverableRetentionPanelData builds the model the panel renders: the stored window
// (or the default) plus the notice and CSRF token its htmx form needs.
func (s *Server) recoverableRetentionPanelData(r *http.Request, notice string) map[string]any {
	data := map[string]any{"Notice": notice, "CSRF": csrfCookieValue(r)}
	s.fillRecoverableRetention(data)
	return data
}

// handleUISaveRecoverableRetention persists the Recoverable Items retention window (in
// whole days). The admin sweep purges expired soft-deleted items to match within about a
// minute, no restart. A value of zero or less disables auto-purge (items are kept until
// manually purged); formInt already maps a blank or negative entry to zero.
func (s *Server) handleUISaveRecoverableRetention(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	days := formInt(r, "recoverable_retention_days")
	if err := s.dir.SetRecoverableSettings(directory.RecoverableSettings{RetentionDays: days}); err != nil {
		s.render(w, "recoverable-retention-panel", s.recoverableRetentionPanelData(r, "Could not save the retention setting: "+err.Error()))
		return
	}
	msg := "Recoverable Items retention saved; the sweep purges expired items within a minute, no restart."
	if days <= 0 {
		msg = "Recoverable Items retention set to keep forever; auto-purge is disabled."
	}
	s.render(w, "recoverable-retention-panel", s.recoverableRetentionPanelData(r, msg))
}
