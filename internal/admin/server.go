package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"hermex/internal/directory"
)

// Directory is what the admin server needs from the account directory: password
// authentication, login-to-id resolution, and a user's admin roles.
type Directory interface {
	Authenticate(user, password string) (mailboxPath string, ok bool)
	UserID(login string) (id int64, ok bool, err error)
	AdminRoles(userID int64) ([]directory.AdminRole, error)
	ListDomains() ([]directory.DomainInfo, error)
}

const (
	sessionCookie = "hermex_admin"
	csrfCookie    = "hermex_admin_csrf"
	csrfHeader    = "X-CSRF-Token"
	sessionTTL    = 8 * time.Hour
)

// ctxKey is the context key the auth middleware stores the session claims under.
type ctxKey struct{}

// Server answers the admin API. Build one with NewServer.
type Server struct {
	dir    Directory
	secret []byte
}

// NewServer builds an admin server backed by the directory and signing sessions
// with secret.
func NewServer(dir Directory, secret []byte) *Server {
	return &Server{dir: dir, secret: secret}
}

// Handler returns the admin HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/login", s.handleLogin)
	mux.Handle("POST /admin/logout", s.protect(http.HandlerFunc(s.handleLogout)))
	mux.Handle("GET /admin/whoami", s.protect(http.HandlerFunc(s.handleWhoami)))
	mux.Handle("GET /admin/domains", s.protect(s.requireSystem(s.handleListDomains)))
	return mux
}

// handleLogin authenticates an administrator and, on success, sets the session
// cookie. Authentication requires valid credentials AND at least one admin role
// — a regular mailbox user who authenticates is still refused (403). The login
// must be the account's primary address; an alias is not resolved here.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if _, ok := s.dir.Authenticate(req.Login, req.Password); !ok {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	uid, ok, err := s.dir.UserID(req.Login)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	roles, err := s.adminRoles(uid, ok)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if len(roles) == 0 {
		http.Error(w, "not an administrator", http.StatusForbidden)
		return
	}
	tok := signToken(s.secret, claims{
		Login:  req.Login,
		UserID: uid,
		Expiry: time.Now().Add(sessionTTL).Unix(),
	})
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/admin",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	// The CSRF cookie is readable (not HttpOnly) so the client echoes it in the
	// X-CSRF-Token header on state-changing requests (double-submit).
	csrf := newCSRFToken()
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    csrf,
		Path:     "/admin",
		MaxAge:   int(sessionTTL.Seconds()),
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, map[string]any{"login": req.Login, "roles": roles, "csrfToken": csrf})
}

// handleLogout clears the session cookie.
func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Path: "/admin", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleWhoami reports the authenticated admin's identity and current roles.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	cl := r.Context().Value(ctxKey{}).(claims)
	roles, err := s.dir.AdminRoles(cl.UserID)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"login": cl.Login, "roles": roles})
}

// protect wraps a handler so it runs only with a valid session cookie, and — for
// a state-changing method — a matching CSRF token (double-submit: the
// X-CSRF-Token header must equal the CSRF cookie). It stashes the claims in the
// request context.
func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		cl, err := verifyToken(s.secret, c.Value)
		if err != nil {
			http.Error(w, "invalid session", http.StatusUnauthorized)
			return
		}
		if isUnsafeMethod(r.Method) && !validCSRF(r) {
			http.Error(w, "missing or invalid CSRF token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, cl)))
	})
}

// newCSRFToken mints a random double-submit CSRF token.
func newCSRFToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// isUnsafeMethod reports whether an HTTP method changes state and so needs CSRF
// protection.
func isUnsafeMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// validCSRF reports whether the request carries a CSRF header equal to its CSRF
// cookie (compared in constant time).
func validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	header := r.Header.Get(csrfHeader)
	return header != "" && hmac.Equal([]byte(cookie.Value), []byte(header))
}

// adminRoles returns a resolved user's admin roles, or none when the login did
// not resolve to a user.
func (s *Server) adminRoles(uid int64, resolved bool) ([]directory.AdminRole, error) {
	if !resolved {
		return nil, nil
	}
	return s.dir.AdminRoles(uid)
}

// writeJSON encodes v as the JSON response body.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
