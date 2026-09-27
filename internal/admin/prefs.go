package admin

import (
	"errors"
	"net/http"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// themeCookie caches the operator's theme, so static/theme.js applies it before
// the first paint. The users record, which webmail shares, is where it is stored.
const themeCookie = "admin_theme"

// themeCookieMaxAge keeps the cached theme for a year, as the browser-set one did.
const themeCookieMaxAge = 365 * 24 * 60 * 60

// prefsStore returns the directory's preference capability, when it has one.
func (s *Server) prefsStore() (directory.UserPrefsStore, bool) {
	store, ok := s.dir.(directory.UserPrefsStore)
	return store, ok
}

// setThemeCookie caches theme for static/theme.js; "system" follows the device.
func setThemeCookie(w http.ResponseWriter, theme string) {
	// #nosec G124 -- theme.js reads this cookie before the first paint, so it cannot be HttpOnly; it holds only the theme name
	http.SetCookie(w, &http.Cookie{
		Name: themeCookie, Value: theme, Path: "/admin", MaxAge: themeCookieMaxAge,
		Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// syncThemeCookie wraps the panel so every full page load carries the caller's
// stored theme in the theme cookie, and a theme chosen in webmail or on another
// browser shows here from the first paint. A panel fragment (an htmx request)
// is left alone, since the page around it already applied the theme.
func (s *Server) syncThemeCookie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/ui/") && r.Header.Get("HX-Request") == "" {
			s.writeStoredTheme(w, r)
		}
		next.ServeHTTP(w, r)
	})
}

// writeStoredTheme sets the theme cookie to the signed-in caller's stored theme
// when the request carries a different one. A caller who never chose a theme
// keeps whatever the browser holds.
func (s *Server) writeStoredTheme(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiClaims(r)
	if !ok {
		return
	}
	store, ok := s.prefsStore()
	if !ok {
		return
	}
	p, found, err := store.GetUserPrefs(cl.Login)
	if err != nil {
		s.logger.Emit(logging.Event{
			Level: logging.LevelError, Subsystem: logging.Admin, Name: "prefs.read_fail", Err: err.Error(),
		})
		return
	}
	if !found || p.Theme == "" {
		return
	}
	if c, err := r.Cookie(themeCookie); err == nil && c.Value == p.Theme {
		return
	}
	setThemeCookie(w, p.Theme)
}

// handleUISavePrefs stores the signed-in operator's theme in the users record
// webmail shares. Any panel user may set their own theme, so it asks for a session
// and the CSRF header only.
func (s *Server) handleUISavePrefs(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiClaims(r)
	if !ok {
		http.Error(w, "session expired", http.StatusUnauthorized)
		return
	}
	if !validCSRF(r) {
		http.Error(w, "missing or invalid CSRF token", http.StatusForbidden)
		return
	}
	store, ok := s.prefsStore()
	if !ok {
		http.Error(w, "preferences are not supported", http.StatusNotImplemented)
		return
	}
	theme := r.PostFormValue("theme")
	found, err := store.SetUserPrefs(cl.Login, directory.UserPrefsUpdate{Theme: &theme})
	switch {
	case errors.Is(err, directory.ErrInvalidPref):
		http.Error(w, "invalid theme", http.StatusBadRequest)
	case err != nil:
		s.fail(w, "could not save the theme", err, http.StatusInternalServerError)
	case !found:
		http.Error(w, "no such user", http.StatusNotFound)
	default:
		setThemeCookie(w, theme)
		w.WriteHeader(http.StatusNoContent)
	}
}
