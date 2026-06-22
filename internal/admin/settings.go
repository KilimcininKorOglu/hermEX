package admin

import "net/http"

// handleUISettings renders the unified Settings page: every operator-tunable setting
// in one place, grouped into category tabs (system admins). The individual panels post
// to their existing endpoints and swap in place, so a save never leaves the page.
func (s *Server) handleUISettings(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "settings.html", s.settingsPageData(r, ""))
}

// settingsPageData merges every settings panel's data into one model. It reuses
// antispamPageData (scoring, greylist, rate-limit, outbound, relay, digest, message
// size, model/ruleset status) and adds the protocol size limits and the spam-history
// retention so all panels render on the single page.
func (s *Server) settingsPageData(r *http.Request, notice string) map[string]any {
	data := s.antispamPageData(r, notice)
	data["Nav"] = "settings"

	imapMB := int64(defaultIMAPLiteralMB)
	if sl, found, err := s.dir.GetSizeLimits(); err == nil && found {
		imapMB = sl.IMAPLiteralBytes / (1024 * 1024)
	}
	data["IMAPLiteralMB"] = imapMB

	retain := defaultSpamHistoryRetainDisplay
	if st, found, err := s.dir.GetSpamHistorySettings(); err == nil && found {
		retain = st.Retain
	}
	data["Retain"] = retain
	return data
}
