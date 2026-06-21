package admin

import (
	"net/http"
	"strconv"

	"hermex/internal/directory"
)

// adminPerms resolves a caller's effective permissions through the directory's
// single resolution path (named roles unioned with the equivalents of their
// direct tier grants). It returns an empty set on error, so a resolution
// failure denies rather than grants.
func (s *Server) adminPerms(userID int64) []directory.Permission {
	perms, err := s.dir.EffectivePermissions(userID)
	if err != nil {
		return nil
	}
	return perms
}

// hasPerm reports whether a permission set contains an exact (name, params) pair.
func hasPerm(perms []directory.Permission, name, params string) bool {
	for _, p := range perms {
		if p.Name == name && p.Params == params {
			return true
		}
	}
	return false
}

// isSystemReadAdmin reports whether a user may read every administrative
// resource: a full system administrator or a read-only system administrator.
func (s *Server) isSystemReadAdmin(userID int64) bool {
	perms := s.adminPerms(userID)
	return hasPerm(perms, directory.PermSystemAdmin, "") ||
		hasPerm(perms, directory.PermSystemAdminRO, "")
}

// isReadMethod reports whether an HTTP method only reads state, so a read-only
// administrator may perform it.
func isReadMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead
}

// domainWriteAllowed reports whether a permission set may WRITE a domain's
// users: a full system admin, or a domain admin over all domains or this one.
func domainWriteAllowed(perms []directory.Permission, domainID int64) bool {
	return hasPerm(perms, directory.PermSystemAdmin, "") ||
		hasPerm(perms, directory.PermDomainAdmin, "*") ||
		hasPerm(perms, directory.PermDomainAdmin, strconv.FormatInt(domainID, 10))
}

// domainReadAllowed reports whether a permission set may READ a domain's users:
// write scope, or a read-only admin (system-wide, or domain — all or this one).
func domainReadAllowed(perms []directory.Permission, domainID int64) bool {
	return domainWriteAllowed(perms, domainID) ||
		hasPerm(perms, directory.PermSystemAdminRO, "") ||
		hasPerm(perms, directory.PermDomainAdminRO, "*") ||
		hasPerm(perms, directory.PermDomainAdminRO, strconv.FormatInt(domainID, 10))
}

// scopedReadDomains reports the domains a caller may read: all=true for a system
// read admin or a domain admin over all domains, otherwise the specific id set.
// It drives list filtering so a domain admin sees only their domains' rows.
func (s *Server) scopedReadDomains(userID int64) (all bool, ids map[int64]bool) {
	perms := s.adminPerms(userID)
	if hasPerm(perms, directory.PermSystemAdmin, "") || hasPerm(perms, directory.PermSystemAdminRO, "") ||
		hasPerm(perms, directory.PermDomainAdmin, "*") || hasPerm(perms, directory.PermDomainAdminRO, "*") {
		return true, nil
	}
	ids = map[int64]bool{}
	for _, p := range perms {
		if p.Name == directory.PermDomainAdmin || p.Name == directory.PermDomainAdminRO {
			if id, err := strconv.ParseInt(p.Params, 10, 64); err == nil {
				ids[id] = true
			}
		}
	}
	return false, ids
}

// requireUserScope gates a per-user management route on the target user's domain,
// method-aware: a read admits a read-only domain (or system) admin, a write
// requires domain write scope. The target is the {email} path value; an unknown
// user is 404. Role-assignment routes are deliberately NOT wrapped by this — they
// stay full-system-admin-only so a domain admin cannot grant itself authority.
func (s *Server) requireUserScope(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, found, err := s.dir.GetUser(r.PathValue("email"))
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		if !found {
			http.Error(w, "no such user", http.StatusNotFound)
			return
		}
		perms := s.adminPerms(claimsOf(r).UserID)
		allowed := domainWriteAllowed(perms, u.DomainID)
		if isReadMethod(r.Method) {
			allowed = domainReadAllowed(perms, u.DomainID)
		}
		if !allowed {
			http.Error(w, "forbidden: requires an administrator of this user's domain", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// requirePasswordScope gates the password-reset endpoint: a ResetPasswd holder
// (any user, the additive capability), or an administrator with write scope over
// the target user's domain (which covers a full system admin). A read-only admin
// without ResetPasswd is refused, so the capability is additive rather than a
// write bypass.
func (s *Server) requirePasswordScope(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		perms := s.adminPerms(claimsOf(r).UserID)
		if hasPerm(perms, directory.PermResetPasswd, "") {
			next(w, r)
			return
		}
		u, found, err := s.dir.GetUser(r.PathValue("email"))
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		if !found {
			http.Error(w, "no such user", http.StatusNotFound)
			return
		}
		if domainWriteAllowed(perms, u.DomainID) {
			next(w, r)
			return
		}
		http.Error(w, "forbidden: requires password-reset authority", http.StatusForbidden)
	}
}
