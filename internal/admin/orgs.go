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
