package admin

import (
	"net/http"
	"strconv"

	"hermex/internal/directory"
)

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
		"Nav":   "users",
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

// handleUIUserDetail renders one user's detail/edit page (system administrators
// only). The user is named in the path.
func (s *Server) handleUIUserDetail(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	s.render(w, "user_detail.html", map[string]any{
		"Nav":  "users",
		"CSRF": csrfCookieValue(r),
		"User": u,
	})
}

// handleUIUserEdit saves the edited account fields and returns the refreshed
// status panel for htmx to swap in; a directory error is reported in the panel
// rather than failing the request.
func (s *Server) handleUIUserEdit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	atoi := func(v string) int { n, _ := strconv.Atoi(v); return n }
	found, err := s.dir.UpdateUser(r.PathValue("email"), directory.UserUpdate{
		Status:      atoi(r.PostFormValue("status")),
		Lang:        r.PostFormValue("lang"),
		Timezone:    r.PostFormValue("timezone"),
		DisplayType: atoi(r.PostFormValue("displayType")),
		Homeserver:  atoi(r.PostFormValue("homeserver")),
		POP3IMAP:    r.PostFormValue("pop3_imap") != "",
		SMTP:        r.PostFormValue("smtp") != "",
	})
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = "Could not save: " + err.Error()
	case !found:
		data["Error"] = "No such user."
	default:
		data["Saved"] = true
	}
	s.render(w, "user-status", data)
}

// handleUIUserDelete deletes the user and redirects the browser back to the user
// list via htmx. The mailbox files are removed only when the deleteFiles checkbox
// is set.
func (s *Server) handleUIUserDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	deleteFiles := r.PostFormValue("deleteFiles") != ""
	if _, err := s.dir.DeleteUser(r.PathValue("email"), deleteFiles); err != nil {
		http.Error(w, "could not delete user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/admin/ui/users")
	w.WriteHeader(http.StatusOK)
}
