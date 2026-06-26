package admin

import (
	"maps"
	"net/http"
	"strconv"

	"hermex/internal/directory"
)

// handleUIDomains renders the domains management page (system administrators only).
func (s *Server) handleUIDomains(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	domains, _ := s.dir.ListDomains()
	def, _, _ := s.dir.GetCreateDefaults(0)
	s.render(w, "domains.html", map[string]any{
		"Nav": "domains", "CSRF": csrfCookieValue(r), "Domains": domains,
		"DefaultMaxUser": def.Domain.MaxUser,
	})
}

// handleUICreateDomain creates a domain from the management form and returns the
// refreshed panel for htmx to swap in. The form carries the maximum-users limit
// (pre-filled from the system create-default); a positive value is applied to the
// new domain.
func (s *Server) handleUICreateDomain(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	name := r.PostFormValue("name")
	maxUser, _ := strconv.ParseInt(r.PostFormValue("maxUser"), 10, 64)
	var errMsg string
	if name == "" {
		errMsg = "A domain name is required."
	} else if id, err := s.dir.CreateDomain(name, s.paths.HomedirFor(name)); err != nil {
		errMsg = "Could not create domain: " + err.Error()
	} else if maxUser > 0 {
		if _, err := s.dir.UpdateDomain(id, directory.DomainUpdate{MaxUser: maxUser}); err != nil {
			errMsg = "Created the domain, but could not set the user limit: " + err.Error()
		}
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
	// From the domain detail page the domain is gone, so navigate back to the
	// list; from the list page swap the refreshed panel in place.
	if errMsg == "" && r.PostFormValue("from") == "detail" {
		w.Header().Set("HX-Redirect", "/admin/ui/domains")
		w.WriteHeader(http.StatusOK)
		return
	}
	domains, _ := s.dir.ListDomains()
	s.render(w, "domains-panel", map[string]any{"Domains": domains, "Error": errMsg, "CSRF": csrfCookieValue(r)})
}

// handleUIDomainDetail renders one domain's management page: edit its status,
// organization, mailbox cap, and contact fields, see its user counts, and purge
// it (system administrators only).
func (s *Server) handleUIDomainDetail(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	dd, found, err := s.dir.GetDomain(id)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such domain", http.StatusNotFound)
		return
	}
	orgs, _ := s.dir.ListOrgs()
	policy, _ := s.dir.GetDomainSyncPolicy(dd.Name)
	override, _, _ := s.dir.GetCreateDefaults(dd.ID)
	users, _ := s.dir.ListUsersInDomain(id)
	contacts, _ := s.dir.ListContactsInDomain(id)
	groups, _ := s.dir.ListMListsInDomain(id)
	spamThreshold, _ := s.dir.GetDomainSpamThreshold(dd.Name)
	branding, _, _ := s.dir.GetDomainBranding(dd.Name)
	senderInt, senderExt, _ := s.dir.GetDomainNameTemplates(dd.Name)
	avIn, avOut, _ := s.dir.GetDomainAVScan(dd.Name)
	data := map[string]any{
		"Nav": "domains", "CSRF": csrfCookieValue(r), "Domain": dd, "Orgs": orgs,
		"PolicyFields":       policyView(policy),
		"Override":           userOverrideViewOf(override.User),
		"DomainUsers":        users,
		"DomainContacts":     contacts,
		"DomainGroups":       groups,
		"SpamThreshold":      spamThreshold,
		"Branding":           branding,
		"SenderNameInternal": senderInt,
		"SenderNameExternal": senderExt,
		"AVScanInbound":      avIn,
		"AVScanOutbound":     avOut,
	}
	maps.Copy(data, s.dkimData(dd.Name))
	// Prescribe the DNS records the domain owner must publish, reusing the DKIM
	// record already merged above (empty when no key exists yet) and adding the
	// MTA-STS/TLSRPT records when publishing is enabled.
	dkimName, _ := data["DKIMRecordName"].(string)
	dkimValue, _ := data["DKIMPublicTXT"].(string)
	sts, _, _ := s.dir.GetMTASTSSettings()
	data["DNSRecords"] = prescribeDomainDNS(dd.Name, s.paths.ServerHostname(), dkimName, dkimValue, sts)
	s.render(w, "domain_detail.html", data)
}

// handleUISaveDomain saves a domain's edited fields from the detail form and
// returns the refreshed status panel. The form carries every field, so the write
// is a full replace (no read-merge needed); the organization is applied separately
// through AssignDomainToOrg (0 detaches).
func (s *Server) handleUISaveDomain(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	atoi := func(v string) int64 { n, _ := strconv.ParseInt(v, 10, 64); return n }
	data := map[string]any{}
	found, err := s.dir.UpdateDomain(id, directory.DomainUpdate{
		Status:    int(atoi(r.PostFormValue("status"))),
		MaxUser:   atoi(r.PostFormValue("maxUser")),
		Title:     r.PostFormValue("title"),
		Address:   r.PostFormValue("address"),
		AdminName: r.PostFormValue("adminName"),
		Tel:       r.PostFormValue("tel"),
	})
	switch {
	case err != nil:
		data["Error"] = "Could not save: " + err.Error()
	case !found:
		data["Error"] = "No such domain."
	default:
		if _, err := s.dir.AssignDomainToOrg(id, atoi(r.PostFormValue("org"))); err != nil {
			data["Error"] = "Saved the fields, but the organization change failed: " + err.Error()
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
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
