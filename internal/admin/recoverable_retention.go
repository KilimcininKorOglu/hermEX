package admin

import (
	"net/http"

	"hermex/internal/directory"
)

// fillRecoverableRetention sets the Recoverable Items retention window (in days) on a
// page-data map, using the stored value or the Exchange-matching default when none has
// been saved. Shared by the Settings page so its Retention tab can render the panel. A
// failed read is recorded in failed rather than shown as the default.
func (s *Server) fillRecoverableRetention(data map[string]any, failed readFailures) {
	rs, found, err := s.dir.GetRecoverableSettings()
	if !s.noteRead(failed, "recoverable-retention", "what.recoverableRetention", err) {
		return
	}
	days := directory.DefaultRecoverableRetentionDays
	if found {
		days = rs.RetentionDays
	}
	data["RecoverableRetentionDays"] = days
}

// recoverableRetentionPanelData builds the model the panel renders: the stored window
// (or the default) plus the notice and CSRF token its htmx form needs.
func (s *Server) recoverableRetentionPanelData(r *http.Request, notice panelNotice) map[string]any {
	failed := readFailures{}
	data := map[string]any{"Notice": notice, "CSRF": csrfCookieValue(r), "ReadFailed": failed}
	s.fillRecoverableRetention(data, failed)
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
		s.render(w, r, "recoverable-retention-panel", s.recoverableRetentionPanelData(r, s.failNotice("settings.retentionFailed", err)))
		return
	}
	done := "settings.recoverableSaved"
	if days <= 0 {
		done = "settings.recoverableForever"
	}
	s.render(w, r, "recoverable-retention-panel", s.recoverableRetentionPanelData(r, okNotice(done)))
}
