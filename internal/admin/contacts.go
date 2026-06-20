package admin

import (
	"net/http"
	"strings"
)

// handleUIContacts renders the org mail-contacts management page (system
// administrators only). It carries the existing domains so the create form can
// offer them as filing domains.
func (s *Server) handleUIContacts(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	contacts, _ := s.dir.ListContacts()
	domains, _ := s.dir.ListDomains()
	s.render(w, "contacts.html", map[string]any{
		"Nav": "contacts", "CSRF": csrfCookieValue(r),
		"Contacts": contacts, "Domains": domains,
	})
}

// handleUICreateContact creates an org mail contact from the management form and
// returns the refreshed panel for htmx to swap in.
func (s *Server) handleUICreateContact(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	var errMsg string
	switch {
	case email == "" || r.PostFormValue("domain") == "":
		errMsg = "A contact address and a filing domain are required."
	default:
		if _, err := s.dir.CreateContact(email, r.PostFormValue("displayname"), r.PostFormValue("domain")); err != nil {
			errMsg = "Could not create contact: " + err.Error()
		}
	}
	s.renderContactsPanel(w, r, errMsg)
}

// handleUIDeleteContact deletes an org mail contact named in the path and returns
// the refreshed panel for htmx to swap in.
func (s *Server) handleUIDeleteContact(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	var errMsg string
	if _, err := s.dir.DeleteContact(r.PathValue("email")); err != nil {
		errMsg = "Could not delete contact: " + err.Error()
	}
	s.renderContactsPanel(w, r, errMsg)
}

// renderContactsPanel re-renders the contacts table fragment with the current
// list and an optional error. The CSRF token is carried so the per-row delete
// forms in the swapped-in fragment keep working.
func (s *Server) renderContactsPanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	contacts, _ := s.dir.ListContacts()
	s.render(w, "contacts-panel", map[string]any{
		"Contacts": contacts, "CSRF": csrfCookieValue(r), "Error": errMsg,
	})
}
