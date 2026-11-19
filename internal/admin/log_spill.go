package admin

import "net/http"

// fillLogSpill sets the per-daemon log spill cap (in MiB) on a page-data map. A
// failed read is recorded in failed, since showing the default would claim a cap
// that may not be the one in force.
func (s *Server) fillLogSpill(data map[string]any, failed readFailures) {
	mb, err := s.dir.GetLogSpillLimitMB()
	if !s.noteRead(failed, "log-spill", "what.logSpill", err) {
		return
	}
	data["LogSpillLimitMB"] = mb
}

// logSpillPanelData builds the model the log-spill panel renders.
func (s *Server) logSpillPanelData(r *http.Request, notice panelNotice) map[string]any {
	failed := readFailures{}
	data := map[string]any{"Notice": notice, "CSRF": csrfCookieValue(r), "ReadFailed": failed}
	s.fillLogSpill(data, failed)
	return data
}

// handleUISaveLogSpill persists the cap on each daemon's log spill file, the file
// a daemon writes its events to while the central log store is unreachable. Every
// daemon applies it within about a minute, no restart. Zero removes the cap; an
// entry that is not a whole number of MiB is refused and nothing is stored.
func (s *Server) handleUISaveLogSpill(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	mb := formInt(r, "log_spill_limit_mb")
	if mb < 0 {
		s.render(w, r, "log-spill-panel", s.logSpillPanelData(r, errorNotice("settings.logSpillInvalid")))
		return
	}
	if err := s.dir.SetLogSpillLimitMB(mb); err != nil {
		s.render(w, r, "log-spill-panel", s.logSpillPanelData(r, s.failNotice("settings.logSpillFailed", err)))
		return
	}
	done := "settings.logSpillSaved"
	if mb == 0 {
		done = "settings.logSpillUnlimited"
	}
	s.render(w, r, "log-spill-panel", s.logSpillPanelData(r, okNotice(done)))
}
