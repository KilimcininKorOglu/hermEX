package webmail2api

import (
	"net/http"

	"hermex/internal/directory"
)

// handleChangePassword verifies the caller's current password and stores a new
// one, gated on the change-password privilege and a non-LDAP account (an LDAP
// password is owned by the directory, not us). Mirrors the server-rendered
// webmail's self-service password change.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if !s.passwordChangeAllowed(c.Email) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "changing your password is disabled for this account"})
		return
	}
	setter, ok := s.auth.(directory.PasswordSetter)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "self-service password change is not available"})
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the new password must be at least 8 characters"})
		return
	}
	// Verify the current password with the lenient path: a must-change account is
	// the very caller this flow exists for, so the strict Authenticate (which
	// denies it) must not block proving the temporary password.
	if _, ok := s.authenticateForChange(c.Email, req.CurrentPassword); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "your current password is incorrect"})
		return
	}
	if ok, err := setter.SetPassword(c.Email, req.NewPassword); err != nil || !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not change your password"})
		return
	}
	// Changing your own password satisfies (and clears) any admin-forced change
	// requirement set by a password reset.
	if clr, ok := s.auth.(interface {
		RequirePasswordChange(string, bool) (bool, error)
	}); ok {
		_, _ = clr.RequirePasswordChange(c.Email, false)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// passwordChangeAllowed reports whether the user may change their own password:
// the change-password privilege is set and the account is not LDAP-backed.
func (s *Server) passwordChangeAllowed(user string) bool {
	if pr, ok := s.auth.(interface {
		Privileges(string) (directory.ServicePrivileges, bool)
	}); ok {
		if privs, _ := pr.Privileges(user); !privs.ChgPasswd {
			return false
		}
	}
	if lu, ok := s.auth.(interface{ IsLDAPUser(string) (bool, error) }); ok {
		if ldap, err := lu.IsLDAPUser(user); err != nil || ldap {
			return false
		}
	}
	return true
}
