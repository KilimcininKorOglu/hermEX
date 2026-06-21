package admin

import (
	"net/http"
	"strconv"
	"strings"

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

// aliasValueScopeError reports the first alias or alternative-name value whose
// e-mail domain the caller may not write to, or ok=true when every value is in
// scope. It closes a value-namespace hole that requireUserScope cannot see:
// requireUserScope authorizes the TARGET user's domain, but an alias/altname is a
// SECOND address written into the global resolver (resolve() and GetForward() match
// inbound mail and logins over username/altname/alias). Without this check a domain
// admin could edit an in-scope user yet set a value in a foreign served domain
// (e.g. aliasing alice@acme.test to ceo@victim.test), silently redirecting that
// domain's mail and logins to the attacker's mailbox.
//
// A full system administrator is unrestricted (every value passes), matching their
// authority over the whole deployment. For any other caller, a domain-qualified
// value must name a SERVED domain the caller administers (domainWriteAllowed) — a
// domain admin over "*" passes any served domain, a scoped one only its own. Bare
// values (no "@", used as alternative login names) carry no domain to escape into
// and are always allowed. On a directory-resolution failure it denies (fail closed).
func (s *Server) aliasValueScopeError(perms []directory.Permission, values []string) (bad string, ok bool) {
	if hasPerm(perms, directory.PermSystemAdmin, "") {
		return "", true
	}
	domains, err := s.dir.ListDomains()
	if err != nil {
		return "", false
	}
	idByName := make(map[string]int64, len(domains))
	for _, d := range domains {
		idByName[strings.ToLower(d.Name)] = d.ID
	}
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		at := strings.LastIndex(v, "@")
		if at < 0 {
			continue // bare login name: no domain to claim
		}
		id, served := idByName[v[at+1:]]
		if !served || !domainWriteAllowed(perms, id) {
			return v, false
		}
	}
	return "", true
}

// scopeRefusal builds the 403 message for an out-of-scope alias/altname value. It
// names the offending address when known, falling back to a generic message when
// the denial came from a resolution failure (bad == "").
func scopeRefusal(kind, bad string) string {
	if bad == "" {
		return "forbidden: an " + kind + " is outside your administrative domains"
	}
	return "forbidden: " + kind + " " + bad + " is outside your administrative domains"
}

// requirePurge gates the destructive domain-purge endpoint: a full system admin,
// or any holder of the DomainPurge capability. No other admin — not even a domain
// admin of the target — may purge, matching the capability's all-or-nothing scope.
func (s *Server) requirePurge(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		perms := s.adminPerms(claimsOf(r).UserID)
		if hasPerm(perms, directory.PermSystemAdmin, "") || hasPerm(perms, directory.PermDomainPurge, "") {
			next(w, r)
			return
		}
		http.Error(w, "forbidden: requires domain-purge authority", http.StatusForbidden)
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
