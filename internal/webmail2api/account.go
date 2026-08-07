package webmail2api

import (
	"log"
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
	revoked := s.revokeOtherSessions(c.Email, c.Jti)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revokedSessions": revoked})
}

// revokeOtherSessions ends every signed-in browser but the caller's own, and
// reports how many it ended.
//
// A changed password does not invalidate an already-issued token: the per-request
// check only asks whether the session row still exists. So an attacker holding a
// stolen cookie survived the victim changing their password, for the rest of the
// token's lifetime, which defeats the whole point of the remediation.
//
// It is best-effort by design. The password is already changed by this point, and
// failing the response would tell the user their change did not happen when it
// did; the count in the response is what tells them whether the eviction ran.
func (s *Server) revokeOtherSessions(email, keepJti string) int64 {
	store, ok := s.auth.(sessionStore)
	if !ok {
		return 0 // stateless sessions: nothing is recorded, so nothing can be revoked
	}
	n, err := store.DeleteOtherWebmailSessions(email, keepJti)
	if err != nil {
		log.Printf("webmail2: could not revoke other sessions for %s after a password change: %v", email, err)
		return 0
	}
	return n
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
