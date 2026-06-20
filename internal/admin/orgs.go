package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"hermex/internal/directory"
)

// hasOrgScope reports whether a user may administer an organization: a system
// admin may administer any, an org admin only the one its role is scoped to.
func (s *Server) hasOrgScope(userID, orgID int64) bool {
	roles, err := s.dir.AdminRoles(userID)
	if err != nil {
		return false
	}
	for _, role := range roles {
		if role.Role == directory.AdminSystem {
			return true
		}
		if role.Role == directory.AdminOrg && role.ScopeID == orgID {
			return true
		}
	}
	return false
}

// orgScope parses the {orgID} path value and authorizes the caller for it. When
// ok is false a response has already been written.
func (s *Server) orgScope(w http.ResponseWriter, r *http.Request) (orgID int64, ok bool) {
	orgID, err := strconv.ParseInt(r.PathValue("orgID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid organization id", http.StatusBadRequest)
		return 0, false
	}
	if !s.hasOrgScope(claimsOf(r).UserID, orgID) {
		http.Error(w, "forbidden: requires an administrator of this organization", http.StatusForbidden)
		return 0, false
	}
	return orgID, true
}

// orgIDParam parses the {orgID} path value, writing a 400 when it is not a
// number. The org-management routes are system-admin only (their requireSystem
// wrapper authorizes), so this only validates the format.
func orgIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("orgID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid organization id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// orgInput is the JSON body for creating or updating an organization.
type orgInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// handleListOrgs returns every organization with its domain count.
func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	orgs, err := s.dir.ListOrgs()
	if err != nil {
		http.Error(w, "could not list organizations", http.StatusInternalServerError)
		return
	}
	writeJSON(w, orgs)
}

// handleCreateOrg creates an organization from the JSON body.
func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	var in orgInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	id, err := s.dir.CreateOrg(in.Name, in.Description)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]int64{"id": id})
}

// handleGetOrg returns one organization, 404 for an unknown id.
func (s *Server) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	id, ok := orgIDParam(w, r)
	if !ok {
		return
	}
	org, found, err := s.dir.GetOrg(id)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such organization", http.StatusNotFound)
		return
	}
	writeJSON(w, org)
}

// handleUpdateOrg replaces an organization's name and description.
func (s *Server) handleUpdateOrg(w http.ResponseWriter, r *http.Request) {
	id, ok := orgIDParam(w, r)
	if !ok {
		return
	}
	var in orgInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	found, err := s.dir.UpdateOrg(id, in.Name, in.Description)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !found {
		http.Error(w, "no such organization", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteOrg deletes an organization; its domains become organizationless
// and its org-scoped configuration is removed. Deleting the reserved id 0 is
// refused by the directory and surfaces as a 400.
func (s *Server) handleDeleteOrg(w http.ResponseWriter, r *http.Request) {
	id, ok := orgIDParam(w, r)
	if !ok {
		return
	}
	found, err := s.dir.DeleteOrg(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !found {
		http.Error(w, "no such organization", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAssignOrgDomain attaches the domain named in the path to the organization
// named in the path.
func (s *Server) handleAssignOrgDomain(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgIDParam(w, r)
	if !ok {
		return
	}
	domID, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	found, err := s.dir.AssignDomainToOrg(domID, orgID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !found {
		http.Error(w, "no such domain", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUnassignOrgDomain detaches the domain named in the path from its
// organization (org_id 0).
func (s *Server) handleUnassignOrgDomain(w http.ResponseWriter, r *http.Request) {
	if _, ok := orgIDParam(w, r); !ok {
		return
	}
	domID, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	found, err := s.dir.AssignDomainToOrg(domID, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !found {
		http.Error(w, "no such domain", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetLDAP returns an organization's LDAP configuration. The bind password
// is never disclosed — only whether one is stored.
func (s *Server) handleGetLDAP(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.orgScope(w, r)
	if !ok {
		return
	}
	cfg, found, err := s.dir.GetLDAPConfig(orgID)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no LDAP configuration for this organization", http.StatusNotFound)
		return
	}
	writeJSON(w, struct {
		URI             string
		StartTLS        bool
		BindDN          string
		BaseDN          string
		UsernameAttr    string
		BindPasswordSet bool
	}{cfg.URI, cfg.StartTLS, cfg.BindDN, cfg.BaseDN, cfg.UsernameAttr, cfg.BindPassword != ""})
}

// handlePutLDAP sets an organization's LDAP configuration. A request that omits
// the bind password keeps the stored one, so the secret need not round-trip
// through the client.
func (s *Server) handlePutLDAP(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.orgScope(w, r)
	if !ok {
		return
	}
	var cfg directory.LDAPConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if cfg.BindPassword == "" {
		if existing, found, _ := s.dir.GetLDAPConfig(orgID); found {
			cfg.BindPassword = existing.BindPassword
		}
	}
	if err := s.dir.SetLDAPConfig(orgID, cfg); err != nil {
		http.Error(w, "could not save LDAP configuration", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
