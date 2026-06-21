package admin

import (
	"net/http"

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

// requireWriteOrReset gates a mutation that the ResetPasswd capability also
// authorizes (the password-reset endpoint): a full system administrator, or any
// holder of ResetPasswd. A read-only administrator without ResetPasswd is
// refused, so the capability is additive on top of read-only access rather than
// a write bypass.
func (s *Server) requireWriteOrReset(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		perms := s.adminPerms(claimsOf(r).UserID)
		if hasPerm(perms, directory.PermSystemAdmin, "") ||
			hasPerm(perms, directory.PermResetPasswd, "") {
			next(w, r)
			return
		}
		http.Error(w, "forbidden: requires password-reset authority", http.StatusForbidden)
	}
}
