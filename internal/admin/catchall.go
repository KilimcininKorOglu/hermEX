package admin

import (
	"net/http"
	"strings"

	"hermex/internal/logging"
)

// handleUISaveDomainCatchAll names the account a domain collects unknown recipients in, or
// clears it when the form selects none. It is a system administrator action, like the
// domain's other settings, and returns the shared save-status partial for htmx.
func (s *Server) handleUISaveDomainCatchAll(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiAuthorized(w, r)
	if !ok {
		return
	}
	dd, ok := s.dkimDomain(w, r)
	if !ok {
		return
	}
	data := map[string]any{}
	old, _, oldErr := s.dir.GetDomainCatchAll(dd.Name)
	address := strings.TrimSpace(r.PostFormValue("catchall"))
	if err := s.dir.SetDomainCatchAll(dd.Name, address); err != nil {
		data["Error"] = s.notice("domainDetail.catchAllFailed", err)
		s.render(w, r, "user-status", data)
		return
	}
	data["Saved"] = true
	s.auditSettingChange(cl.Login, "catchall", logging.Fields{
		"domain": dd.Name, "old": auditOld(old, oldErr), "new": address,
	})
	s.render(w, r, "user-status", data)
}
