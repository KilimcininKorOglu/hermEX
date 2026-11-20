package webmail2api

import (
	"fmt"
	"net/http"
	"unicode/utf8"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/serve"
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
	// Checked before the current password, so a too-short attempt costs the
	// caller none of the sign-in throttle's budget.
	if !s.newPasswordLongEnough(w, c.Email, req.NewPassword) {
		return
	}
	if !s.currentPasswordOK(w, r, c.Email, req.CurrentPassword) {
		return
	}
	if !storeNewPassword(w, setter, c.Email, req.NewPassword) {
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

// newPasswordLongEnough checks a new password against the minimum and writes the
// refusal when it is shorter or the minimum cannot be read.
func (s *Server) newPasswordLongEnough(w http.ResponseWriter, email, password string) bool {
	minLen, err := s.minPasswordLength()
	if err != nil {
		logError("read-password-policy", err, logging.Fields{"user": email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not change your password"})
		return false
	}
	if utf8.RuneCountInString(password) < minLen {
		writeTooShort(w, minLen)
		return false
	}
	return true
}

// storeNewPassword stores the new password and writes the refusal when the store
// fails. The minimum may have been raised since newPasswordLongEnough read it, so
// a too-short refusal from the directory is still the client's error.
func storeNewPassword(w http.ResponseWriter, setter directory.PasswordSetter, email, password string) bool {
	ok, err := setter.SetPassword(email, password)
	if n, short := directory.MinPasswordLength(err); short {
		writeTooShort(w, n)
		return false
	}
	if err != nil || !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not change your password"})
		return false
	}
	return true
}

// minPasswordLength returns the minimum a new password must meet: the
// directory's stored setting, or its default for a directory that keeps none. A
// minimum that cannot be read is an error, never "no minimum".
func (s *Server) minPasswordLength() (int, error) {
	p, ok := s.auth.(interface {
		GetMinPasswordLength() (int, bool, error)
	})
	if !ok {
		return directory.DefaultMinPasswordLength, nil
	}
	n, _, err := p.GetMinPasswordLength()
	return n, err
}

// writeTooShort answers a password under the minimum with a 400 that names the
// minimum, both in the message and as a number the SPA can show.
func writeTooShort(w http.ResponseWriter, minLen int) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error":     fmt.Sprintf("the new password must be at least %d characters", minLen),
		"minLength": minLen,
	})
}

// currentPasswordOK checks the caller's current password and writes the refusal
// when it fails. The check is a credential test like a sign-in, so it runs under
// the same throttle: without it a stolen session could guess the account's
// password here without limit.
func (s *Server) currentPasswordOK(w http.ResponseWriter, r *http.Request, email, password string) bool {
	addr := serve.ClientAddr(r)
	if !s.limiter.Allowed(addr, email) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed attempts, try again later"})
		return false
	}
	// The lenient path: a must-change account is the very caller this flow exists
	// for, so the strict Authenticate (which denies it) must not block proving the
	// temporary password.
	if _, ok := s.authenticateForChange(email, password); !ok {
		s.limiter.Fail(addr, email)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "your current password is incorrect"})
		return false
	}
	s.limiter.Succeed(addr, email)
	return true
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
		logError("revoke-other-sessions", err, logging.Fields{"user": email})
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
