package admin

import "net/http"

// handleUISettings renders the unified Settings page: every operator-tunable setting
// in one place, grouped into category tabs (system admins). The individual panels post
// to their existing endpoints and swap in place, so a save never leaves the page.
func (s *Server) handleUISettings(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, r, "settings.html", s.settingsPageData(r, panelNotice{}))
}

// settingsPageData merges every settings panel's data into one model. It reuses
// antispamPageData (scoring, greylist, rate-limit, outbound, relay, gateway, digest,
// message size, model/ruleset status) and adds the protocol limits, the login
// lockout, the fetch policy and the retention windows so all panels render on the
// single page.
func (s *Server) settingsPageData(r *http.Request, notice panelNotice) map[string]any {
	data := s.antispamPageData(r, notice)
	data["Nav"] = "settings"
	// antispamPageData always sets ReadFailed; the other cards record into it too.
	failed := data["ReadFailed"].(readFailures)
	s.addLimitSettings(data, failed)
	s.fillLogRetention(data, failed)
	s.fillRecoverableRetention(data, failed)
	s.fillSpamRetention(data, failed)
	return data
}
