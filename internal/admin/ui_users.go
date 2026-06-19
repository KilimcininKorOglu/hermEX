package admin

import "net/http"

// uiAuthorized authorizes a UI state-changing request: a valid session, a
// matching CSRF header (the htmx double-submit), and the system admin role. On
// failure it writes an error response and returns ok=false.
func (s *Server) uiAuthorized(w http.ResponseWriter, r *http.Request) (claims, bool) {
	cl, ok := s.uiClaims(r)
	if !ok {
		http.Error(w, "session expired", http.StatusUnauthorized)
		return claims{}, false
	}
	if !validCSRF(r) {
		http.Error(w, "missing or invalid CSRF token", http.StatusForbidden)
		return claims{}, false
	}
	if !s.isSystemAdmin(cl.UserID) {
		http.Error(w, "forbidden: requires a system administrator", http.StatusForbidden)
		return claims{}, false
	}
	return cl, true
}

// handleUIUsers renders the users management page (system administrators only).
func (s *Server) handleUIUsers(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	users, _ := s.dir.ListUsers()
	s.render(w, "users.html", map[string]any{
		"CSRF":  csrfCookieValue(r),
		"Users": users,
	})
}

// handleUICreateUser creates a user from the management form and returns the
// refreshed users panel for htmx to swap in; a validation or directory error is
// reported in the panel rather than failing the request.
func (s *Server) handleUICreateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	email := r.PostFormValue("email")
	var errMsg string
	switch {
	case email == "" || r.PostFormValue("password") == "":
		errMsg = "An email and password are required."
	default:
		if _, err := s.dir.CreateUser(email, r.PostFormValue("password"), s.paths.MaildirFor(email)); err != nil {
			errMsg = "Could not create user: " + err.Error()
		}
	}
	users, _ := s.dir.ListUsers()
	s.render(w, "users-panel", map[string]any{"Users": users, "Error": errMsg})
}
