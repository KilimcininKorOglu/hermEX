package admin

import (
	"net/http"
	"regexp"
	"strconv"

	"hermex/internal/directory"
)

// bindingAllowed reports whether a caller may read (write=false) or edit
// (write=true) one domain's directory binding: an administrator of the bound
// domain (a system administrator included), or an administrator of the domain's
// organization. A read also admits a read-only system or domain administrator.
func (s *Server) bindingAllowed(userID int64, b directory.LDAPBinding, write bool) (bool, error) {
	perms := s.adminPerms(userID)
	if write && domainWriteAllowed(perms, b.DomainID) {
		return true, nil
	}
	if !write && domainReadAllowed(perms, b.DomainID) {
		return true, nil
	}
	return s.orgAdminOfDomain(userID, b.DomainID)
}

// orgAdminOfDomain reports whether a caller administers the organization a domain
// belongs to. An organizationless domain (org 0) has no organization administrator.
func (s *Server) orgAdminOfDomain(userID, domainID int64) (bool, error) {
	dom, found, err := s.dir.GetDomain(domainID)
	if err != nil || !found {
		return false, err
	}
	return dom.OrgID != 0 && s.hasOrgScope(userID, dom.OrgID), nil
}

// bindingScope loads the binding the {id} path value names and authorizes the
// caller for it, method-aware (a read needs read scope, anything else write
// scope). When ok is false a response has already been written.
func (s *Server) bindingScope(w http.ResponseWriter, r *http.Request) (b directory.LDAPBinding, ok bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid binding id", http.StatusBadRequest)
		return b, false
	}
	b, found, err := s.dir.GetLDAPBinding(id)
	if err != nil {
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return b, false
	}
	if !found {
		http.Error(w, "no such binding", http.StatusNotFound)
		return b, false
	}
	allowed, err := s.bindingAllowed(claimsOf(r).UserID, b, !isReadMethod(r.Method))
	if err != nil {
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return b, false
	}
	if !allowed {
		http.Error(w, "forbidden: requires an administrator of this domain", http.StatusForbidden)
		return b, false
	}
	return b, true
}

// attrNamePattern matches an LDAP attribute description without options: an RFC 4512
// descriptor (a letter, then letters, digits and hyphens) or a numeric OID.
var attrNamePattern = regexp.MustCompile(`^(?:[A-Za-z][A-Za-z0-9-]*|(?:0|[1-9][0-9]*)(?:\.(?:0|[1-9][0-9]*))+)$`)

// maxAttrNameLen bounds an attribute name; no registered attribute comes close.
const maxAttrNameLen = 128

// validAttrName reports whether s is a well-formed LDAP attribute name. The name is
// placed into a search filter and an attribute list, so anything else (filter
// syntax, spaces) is refused rather than sent to the directory.
func validAttrName(s string) bool {
	return len(s) <= maxAttrNameLen && attrNamePattern.MatchString(s)
}

// badMappingMsg is the answer to a mapping validMapping refuses.
const badMappingMsg = "the mapping names an unknown field or a malformed LDAP attribute"

// validMapping checks every attribute a mapping names and every field key it sets.
// An empty attribute means the field's standard one and is accepted.
func validMapping(m directory.LDAPMapping) bool {
	known := make(map[string]bool)
	for _, f := range directory.LDAPProfileFields() {
		known[f.Key] = true
	}
	for key, f := range m.Fields {
		if !known[key] || (f.Attr != "" && !validAttrName(f.Attr)) {
			return false
		}
	}
	return m.AliasAttr == "" || validAttrName(m.AliasAttr)
}

// keepSystemFields returns the mapping with its system-only settings taken from the
// stored one: the search bases and the filters decide what the service account reads,
// which can include attributes a domain or organization administrator must not reach.
func keepSystemFields(in, stored directory.LDAPMapping) directory.LDAPMapping {
	in.BaseDN, in.GroupBaseDN, in.GroupFilter = stored.BaseDN, stored.GroupBaseDN, stored.GroupFilter
	in.ContactBaseDN, in.ContactFilter = stored.ContactBaseDN, stored.ContactFilter
	return in
}
