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
	data := s.domainsPanelData(r, "")
	def, _, _ := s.dir.GetCreateDefaults(0)
	data["Nav"] = "domains"
	data["DefaultMaxUser"] = def.Domain.MaxUser
	s.render(w, "domains.html", data)
}

// domainsPanelData returns what the domains list renders: every domain, the
// name of each organization by id so a row names its organization rather than
// a number, and the outcome message of the request.
func (s *Server) domainsPanelData(r *http.Request, errMsg string) map[string]any {
	domains, _ := s.dir.ListDomains()
	orgs, _ := s.dir.ListOrgs()
	orgNames := make(map[int64]string, len(orgs))
	for _, o := range orgs {
		orgNames[o.ID] = o.Name
	}
	return map[string]any{"Domains": domains, "OrgNames": orgNames, "Error": errMsg, "CSRF": csrfCookieValue(r)}
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
		errMsg = s.notice("Could not create domain.", err)
	} else if maxUser > 0 {
		if _, err := s.dir.UpdateDomain(id, directory.DomainUpdate{MaxUser: maxUser}); err != nil {
			errMsg = s.notice("Created the domain, but could not set the user limit.", err)
		}
	}
	s.render(w, "domains-panel", s.domainsPanelData(r, errMsg))
}

// handleUIPurgeDomain purges a domain from the management page and returns the
// refreshed panel; deleteFiles also removes the on-disk mailboxes. It is gated by
// uiAuthorized (full system admin), the same as every other console mutation.
func (s *Server) handleUIPurgeDomain(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	var errMsg string
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		errMsg = "Invalid domain id."
	} else if _, err := s.dir.PurgeDomain(id, r.PostFormValue("deleteFiles") == "true"); err != nil {
		errMsg = s.notice("Could not purge domain.", err)
	}
	// From the domain detail page the domain is gone, so navigate back to the
	// list; from the list page swap the refreshed panel in place.
	if errMsg == "" && r.PostFormValue("from") == "detail" {
		w.Header().Set("HX-Redirect", "/admin/ui/domains")
		w.WriteHeader(http.StatusOK)
		return
	}
	s.render(w, "domains-panel", s.domainsPanelData(r, errMsg))
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
	failed := readFailures{}
	data := map[string]any{"Nav": "domains", "CSRF": csrfCookieValue(r), "Domain": dd, "ReadFailed": failed}
	orgs, err := s.dir.ListOrgs()
	if s.noteRead(failed, "details", "the organizations", err) {
		data["Orgs"] = orgs
	}
	s.addDomainMembers(data, failed, id, dd.Name)
	s.addDomainMailHandling(data, failed, dd.Name)
	s.addDomainPolicies(data, failed, dd)
	maps.Copy(data, s.dkimData(dd.Name))
	s.addDomainGateway(data, failed, dd.Name)
	s.addDomainDNSRecords(data, dd.Name)
	s.render(w, "domain_detail.html", data)
}

// addDomainMembers lists a domain's users, contacts and groups on its detail page,
// or reports a list that could not be read, which the page must not show as empty.
// The catch-all form chooses among the users, so it is hidden when either they or
// the stored catch-all cannot be read.
func (s *Server) addDomainMembers(data map[string]any, failed readFailures, id int64, domain string) {
	users, usersErr := s.dir.ListUsersInDomain(id)
	data["DomainUsers"] = users
	if usersErr != nil {
		data["DomainUsersError"] = s.notice("Could not read the users of this domain.", usersErr)
	}
	contacts, err := s.dir.ListContactsInDomain(id)
	data["DomainContacts"] = contacts
	if err != nil {
		data["DomainContactsError"] = s.notice("Could not read the contacts of this domain.", err)
	}
	groups, err := s.dir.ListMListsInDomain(id)
	data["DomainGroups"] = groups
	if err != nil {
		data["DomainGroupsError"] = s.notice("Could not read the groups of this domain.", err)
	}
	catchAll, _, err := s.dir.GetDomainCatchAll(domain)
	if s.noteRead(failed, "catchall", "the users of this domain", usersErr) &&
		s.noteRead(failed, "catchall", "the catch-all mailbox", err) {
		data["CatchAll"] = catchAll
	}
}

// addDomainMailHandling fills the detail page's mail handling forms, hiding each one
// whose stored value could not be read.
func (s *Server) addDomainMailHandling(data map[string]any, failed readFailures, domain string) {
	threshold, err := s.dir.GetDomainSpamThreshold(domain)
	if s.noteRead(failed, "spam", "the spam threshold", err) {
		data["SpamThreshold"] = threshold
	}
	avIn, avOut, err := s.dir.GetDomainAVScan(domain)
	if s.noteRead(failed, "avscan", "the antivirus settings", err) {
		data["AVScanInbound"], data["AVScanOutbound"] = avIn, avOut
	}
	host, err := s.dir.SplitRelayHost(domain)
	if s.noteRead(failed, "split", "the split domain host", err) {
		data["SplitRelayHost"] = host
	}
	internal, external, err := s.dir.GetDomainNameTemplates(domain)
	if s.noteRead(failed, "sendername", "the outgoing display name templates", err) {
		data["SenderNameInternal"], data["SenderNameExternal"] = internal, external
	}
}

// addDomainPolicies fills the detail page's policy forms, hiding each one whose
// stored value could not be read.
func (s *Server) addDomainPolicies(data map[string]any, failed readFailures, dd directory.DomainDetail) {
	policy, err := s.dir.GetDomainSyncPolicy(dd.Name)
	if s.noteRead(failed, "policy", "the device policy of this domain", err) {
		data["PolicyFields"] = policyView(policy)
	}
	override, _, err := s.dir.GetCreateDefaults(dd.ID)
	if s.noteRead(failed, "override", "the create defaults override", err) {
		data["Override"] = userOverrideViewOf(override.User)
	}
	branding, _, err := s.dir.GetDomainBranding(dd.Name)
	if s.noteRead(failed, "branding", "the login branding", err) {
		data["Branding"] = branding
	}
}

// addDomainDNSRecords prescribes the DNS records the domain owner must publish,
// reusing the DKIM record dkimData merged (empty when no key exists yet) and adding
// the MTA-STS/TLSRPT records when publishing is enabled. When either input could not
// be read no list is built, because it would tell the owner to generate a key that
// may exist, or leave out the MTA-STS records the domain needs.
func (s *Server) addDomainDNSRecords(data map[string]any, domain string) {
	sts, _, err := s.dir.GetMTASTSSettings()
	switch {
	case err != nil:
		data["DNSRecordsError"] = s.notice("Could not read the MTA-STS settings, so the required records cannot be listed.", err)
	case data["DKIMError"] != nil:
		data["DNSRecordsError"] = "Could not read the DKIM key, so the required records cannot be listed."
	default:
		dkimName, _ := data["DKIMRecordName"].(string)
		dkimValue, _ := data["DKIMPublicTXT"].(string)
		data["DNSRecords"] = prescribeDomainDNS(domain, s.paths.ServerHostname(), dkimName, dkimValue, sts)
	}
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
		data["Error"] = s.notice("Could not save.", err)
	case !found:
		data["Error"] = "No such domain."
	default:
		if _, err := s.dir.AssignDomainToOrg(id, atoi(r.PostFormValue("org"))); err != nil {
			data["Error"] = s.notice("Saved the fields, but the organization change failed.", err)
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
			errMsg = s.notice("Could not create alias.", err)
		}
	}
	aliases, _ := s.dir.ListAliases()
	s.render(w, "aliases-panel", map[string]any{"Aliases": aliases, "Error": errMsg})
}
