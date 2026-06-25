package admin

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/directory"
)

// handleUISaveDomainBranding stores a domain's login-page branding from the
// domain-detail form. All-blank fields clear the branding so the domain falls back
// to the global default. It returns the shared save-status partial for htmx.
func (s *Server) handleUISaveDomainBranding(w http.ResponseWriter, r *http.Request) {
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
		b := directory.DomainBranding{
			AppName:      strings.TrimSpace(r.PostFormValue("app_name")),
			LogoURL:      strings.TrimSpace(r.PostFormValue("logo_url")),
			PrimaryColor: strings.TrimSpace(r.PostFormValue("primary_color")),
			Tagline:      strings.TrimSpace(r.PostFormValue("tagline")),
			FooterText:   strings.TrimSpace(r.PostFormValue("footer_text")),
		}
		if err := s.dir.SetDomainBranding(dd.Name, b); err != nil {
			data["Error"] = "Could not save branding: " + err.Error()
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
}
