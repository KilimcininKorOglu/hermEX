package admin

import (
	"net/http"
	"strconv"
)

// handleUISaveDomainSenderName stores a domain's outgoing display-name templates
// (one for internal recipients, one for external) from the domain-detail form. An
// empty template disables customization for that direction. It returns the shared
// save-status partial for htmx.
func (s *Server) handleUISaveDomainSenderName(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	data := map[string]any{}
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		data["Error"] = "Invalid domain id."
		s.render(w, "user-status", data)
		return
	}
	dd, found, err := s.dir.GetDomain(id)
	switch {
	case err != nil:
		data["Error"] = "Server error."
	case !found:
		data["Error"] = "No such domain."
	default:
		if err := s.dir.SetDomainNameTemplates(dd.Name,
			r.PostFormValue("sender_name_internal"), r.PostFormValue("sender_name_external")); err != nil {
			data["Error"] = "Could not save the templates: " + err.Error()
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
}
