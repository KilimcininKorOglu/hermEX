package admin

import (
	"crypto/hmac"
	"io/fs"
	"net/http"
)

// uiClaims returns the session claims for a UI request, or ok=false when the
// caller should redirect to the login page.
func (s *Server) uiClaims(r *http.Request) (claims, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return claims{}, false
	}
	cl, err := verifyToken(s.secret, c.Value)
	if err != nil {
		return claims{}, false
	}
	return cl, true
}

// render writes an HTML template response.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// staticHandler serves the embedded static assets under /admin/static/.
func staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	return http.StripPrefix("/admin/static/", http.FileServer(http.FS(sub)))
}

// handleUILoginPage renders the login form, redirecting an already-signed-in
// admin to the dashboard.
func (s *Server) handleUILoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiClaims(r); ok {
		http.Redirect(w, r, "/admin/ui/", http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", nil)
}

// handleUILoginSubmit authenticates the login form and, on success, starts a
// session and redirects to the dashboard; on failure it re-renders the form.
func (s *Server) handleUILoginSubmit(w http.ResponseWriter, r *http.Request) {
	login := r.PostFormValue("login")
	uid, _, ok, err := s.authAdmin(login, r.PostFormValue("password"))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		s.render(w, "login.html", map[string]any{"Error": "Server error, please try again."})
		return
	}
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "login.html", map[string]any{"Error": "Invalid email or password."})
		return
	}
	session, csrf := s.issueSession(login, uid)
	setSessionCookies(w, session, csrf)
	http.Redirect(w, r, "/admin/ui/", http.StatusSeeOther)
}

// handleUIDashboard renders the dashboard, redirecting to login without a
// session.
func (s *Server) handleUIDashboard(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiClaims(r)
	if !ok {
		http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
		return
	}
	users, _ := s.dir.ListUsers()
	domains, _ := s.dir.ListDomains()
	aliases, _ := s.dir.ListAliases()
	s.render(w, "dashboard.html", map[string]any{
		"Login":       cl.Login,
		"CSRF":        csrfCookieValue(r),
		"UserCount":   len(users),
		"DomainCount": len(domains),
		"AliasCount":  len(aliases),
	})
}

// handleUILogout clears the session — a valid CSRF form token is required — and
// returns to the login page.
func (s *Server) handleUILogout(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiClaims(r); !ok {
		http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
		return
	}
	if !validFormCSRF(r) {
		http.Error(w, "missing or invalid CSRF token", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/admin", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Path: "/admin", MaxAge: -1, Secure: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
}

// csrfCookieValue returns the request's CSRF token, or empty when absent.
func csrfCookieValue(r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil {
		return c.Value
	}
	return ""
}

// validFormCSRF reports whether the request's _csrf form field equals its CSRF
// cookie (compared in constant time) — the form equivalent of the header
// double-submit the JSON API uses.
func validFormCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	return hmac.Equal([]byte(cookie.Value), []byte(r.PostFormValue("_csrf")))
}
