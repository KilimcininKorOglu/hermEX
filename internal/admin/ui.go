package admin

import (
	"bytes"
	"crypto/hmac"
	"io/fs"
	"net/http"
	"strings"

	"hermex/internal/logging"
	"hermex/internal/serve"
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
	if !s.sessionActive(cl) {
		return claims{}, false
	}
	// The panel pages sit outside protect(), so they report the operator here.
	serve.SetUser(r, cl.Login)
	return cl, true
}

// uiRequireSystemPage gates a UI read page on a session and read authority: no
// session redirects to login; a caller without system read access gets 403. A
// read-only system administrator is admitted, pages only read, while write
// actions on those pages are gated separately by uiAuthorized. ok=false means a
// response was already written.
func (s *Server) uiRequireSystemPage(w http.ResponseWriter, r *http.Request) bool {
	cl, ok := s.uiClaims(r)
	if !ok {
		http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
		return false
	}
	if !s.isSystemReadAdmin(cl.UserID) {
		http.Error(w, "forbidden: requires a system administrator", http.StatusForbidden)
		return false
	}
	return true
}

// render writes an HTML template response.
//
// The template is executed into a buffer first. Executing straight into the
// response writer commits the 200 and part of the body before a failure partway
// through can be noticed, so the error branch could only append an error to a page
// already on the wire: the operator got a silently truncated page under a stale
// 200, which for a management UI is the worst outcome, since a half-rendered form
// misrepresents state. Buffering costs one page of memory and makes the failure a
// clean 500 carrying none of the partial render.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.logger.Emit(logging.Event{
			Level: logging.LevelError, Subsystem: logging.Admin, Name: "render.fail",
			Fields: logging.Fields{"template": name}, Err: err.Error(),
		})
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
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
	serve.SetUser(r, login)
	addr := serve.ClientAddr(r)
	if !s.limiter.Allowed(addr, login) {
		w.WriteHeader(http.StatusTooManyRequests)
		s.render(w, "login.html", map[string]any{"Error": "Too many failed attempts, try again later."})
		return
	}
	uid, _, ok, err := s.authAdmin(login, r.PostFormValue("password"))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		s.render(w, "login.html", map[string]any{"Error": "Server error, please try again."})
		return
	}
	if !ok {
		s.limiter.Fail(addr, login)
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "login.html", map[string]any{"Error": "Invalid email or password."})
		return
	}
	s.limiter.Succeed(addr, login)
	session, csrf := s.issueSession(login, uid)
	setSessionCookies(w, session, csrf)
	http.Redirect(w, r, "/admin/ui/", http.StatusSeeOther)
}

// handleUIDashboard renders the dashboard, redirecting to login without a
// session. The counts are scoped to what the caller may read: this page is the
// panel's first screen and the only one a domain-scoped admin reaches without a
// permission check, so unscoped totals would tell them how many mailboxes,
// domains and aliases every other tenant has.
func (s *Server) handleUIDashboard(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiClaims(r)
	if !ok {
		http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
		return
	}
	counts, err := s.dashboardCounts(cl.UserID)
	if err != nil {
		// A failed query must not read as an empty deployment: report it rather
		// than rendering three confident zeroes.
		s.render(w, "dashboard.html", map[string]any{
			"Nav": "dashboard", "Login": cl.Login, "CSRF": csrfCookieValue(r),
			"Error": s.notice("Could not read the directory.", err),
		})
		return
	}
	s.render(w, "dashboard.html", map[string]any{
		"Nav":         "dashboard",
		"Login":       cl.Login,
		"CSRF":        csrfCookieValue(r),
		"UserCount":   counts.users,
		"DomainCount": counts.domains,
		"AliasCount":  counts.aliases,
	})
}

// dashboardCount holds the three headline numbers, already scoped.
type dashboardCount struct{ users, domains, aliases int }

// dashboardCounts totals the users, domains and aliases the caller may read. A
// system (or all-domains) admin counts everything; anyone else counts only their
// own domains. Aliases carry no domain id, so they are matched by the domain part
// of the alias address against the caller's domain names.
func (s *Server) dashboardCounts(userID int64) (dashboardCount, error) {
	users, err := s.dir.ListUsers()
	if err != nil {
		return dashboardCount{}, err
	}
	domains, err := s.dir.ListDomains()
	if err != nil {
		return dashboardCount{}, err
	}
	aliases, err := s.dir.ListAliases()
	if err != nil {
		return dashboardCount{}, err
	}
	all, ids := s.scopedReadDomains(userID)
	if all {
		return dashboardCount{users: len(users), domains: len(domains), aliases: len(aliases)}, nil
	}

	var out dashboardCount
	names := map[string]bool{}
	for _, d := range domains {
		if ids[d.ID] {
			out.domains++
			names[strings.ToLower(d.Name)] = true
		}
	}
	for _, u := range users {
		if ids[u.DomainID] {
			out.users++
		}
	}
	for _, a := range aliases {
		if at := strings.LastIndex(a.Alias, "@"); at >= 0 && names[strings.ToLower(a.Alias[at+1:])] {
			out.aliases++
		}
	}
	return out, nil
}

// handleUILogout clears the session, a valid CSRF form token is required, and
// returns to the login page.
func (s *Server) handleUILogout(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiClaims(r)
	if !ok {
		http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
		return
	}
	if !validFormCSRF(r) {
		http.Error(w, "missing or invalid CSRF token", http.StatusForbidden)
		return
	}
	// Clearing the cookies only asks the browser to forget the token; anyone who
	// captured it keeps a working session until it expires. Revoke the row too.
	s.revokeSession(cl)
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
// cookie (compared in constant time), the form equivalent of the header
// double-submit the JSON API uses.
func validFormCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	return hmac.Equal([]byte(cookie.Value), []byte(r.PostFormValue("_csrf")))
}
