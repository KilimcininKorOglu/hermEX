package admin

import (
	"net/http"
	"strconv"
)

// handleUIDomains renders the domains management page (system administrators only).
func (s *Server) handleUIDomains(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	domains, _ := s.dir.ListDomains()
	s.render(w, "domains.html", map[string]any{"Nav": "domains", "CSRF": csrfCookieValue(r), "Domains": domains})
}

// handleUICreateDomain creates a domain from the management form and returns the
// refreshed panel for htmx to swap in.
func (s *Server) handleUICreateDomain(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	name := r.PostFormValue("name")
	var errMsg string
	if name == "" {
		errMsg = "A domain name is required."
	} else if _, err := s.dir.CreateDomain(name, s.paths.HomedirFor(name)); err != nil {
		errMsg = "Could not create domain: " + err.Error()
	}
	domains, _ := s.dir.ListDomains()
	s.render(w, "domains-panel", map[string]any{"Domains": domains, "Error": errMsg, "CSRF": csrfCookieValue(r)})
}

// handleUIPurgeDomain purges a domain from the management page and returns the
// refreshed panel; deleteFiles also removes the on-disk mailboxes. It is gated by
// uiAuthorized (full system admin) — the same as every other console mutation.
func (s *Server) handleUIPurgeDomain(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	var errMsg string
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		errMsg = "Invalid domain id."
	} else if _, err := s.dir.PurgeDomain(id, r.PostFormValue("deleteFiles") == "true"); err != nil {
		errMsg = "Could not purge domain: " + err.Error()
	}
	domains, _ := s.dir.ListDomains()
	s.render(w, "domains-panel", map[string]any{"Domains": domains, "Error": errMsg, "CSRF": csrfCookieValue(r)})
}

// handleUIAliases renders the aliases management page (system administrators only).
func (s *Server) handleUIAliases(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	aliases, _ := s.dir.ListAliases()
	s.render(w, "aliases.html", map[string]any{"Nav": "aliases", "CSRF": csrfCookieValue(r), "Aliases": aliases})
}

// handleUICreateAlias creates an alias from the management form and returns the
// refreshed panel for htmx to swap in.
func (s *Server) handleUICreateAlias(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	alias, main := r.PostFormValue("alias"), r.PostFormValue("main")
	var errMsg string
	switch {
	case alias == "" || main == "":
		errMsg = "Both the alias and the target address are required."
	default:
		if err := s.dir.CreateAlias(alias, main); err != nil {
			errMsg = "Could not create alias: " + err.Error()
		}
	}
	aliases, _ := s.dir.ListAliases()
	s.render(w, "aliases-panel", map[string]any{"Aliases": aliases, "Error": errMsg})
}
