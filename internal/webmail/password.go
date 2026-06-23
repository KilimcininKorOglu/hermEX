package webmail

import (
	"net/http"

	"hermex/internal/directory"
)

// passwordPage is the change-password form's template model.
type passwordPage struct {
	Saved bool
	Error string
}

// handlePasswordForm redirects the former standalone change-password page to its
// tab on the unified settings page (the tab is shown only when the account may
// change its password); the POST endpoint below still serves the form and keeps
// the privilege gate.
func (s *Server) handlePasswordForm(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings?tab=password", http.StatusSeeOther)
}

// handlePasswordSubmit verifies the current password and stores a new one, gated
// on the change-password privilege. A validation failure redirects back to the
// form with an error code rather than revealing which check failed in a way that
// leaks account state.
func (s *Server) handlePasswordSubmit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if privs, _ := s.auth.Privileges(sess.user); !privs.ChgPasswd {
		http.Error(w, "Changing your password is disabled for this account.", http.StatusForbidden)
		return
	}
	setter, ok := s.auth.(directory.PasswordSetter)
	if !ok {
		http.Error(w, "self-service password change is not available", http.StatusNotImplemented)
		return
	}
	current := r.FormValue("current")
	next := r.FormValue("new")
	confirm := r.FormValue("confirm")

	switch {
	case len(next) < 8:
		http.Redirect(w, r, "/settings?tab=password&err=weak", http.StatusSeeOther)
		return
	case next != confirm:
		http.Redirect(w, r, "/settings?tab=password&err=mismatch", http.StatusSeeOther)
		return
	}
	if _, ok := s.auth.Authenticate(sess.user, current); !ok {
		http.Redirect(w, r, "/settings?tab=password&err=current", http.StatusSeeOther)
		return
	}
	if ok, err := setter.SetPassword(sess.user, next); err != nil || !ok {
		http.Redirect(w, r, "/settings?tab=password&err=save", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/settings?tab=password&saved=1", http.StatusSeeOther)
}

// passwordError maps a redirect error code to a human-readable message.
func passwordError(code string) string {
	switch code {
	case "weak":
		return "The new password must be at least 8 characters."
	case "mismatch":
		return "The new password and its confirmation do not match."
	case "current":
		return "Your current password is not correct."
	case "save":
		return "The password could not be saved. Please try again."
	}
	return ""
}
