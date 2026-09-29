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

// prefsCookieMaxAge keeps a cached preference for a year, as the browser-set theme
// cookie did.
const prefsCookieMaxAge = 365 * 24 * 60 * 60

// prefsStore returns the directory's preference capability, when it has one.
func (s *Server) prefsStore() (directory.UserPrefsStore, bool) {
	store, ok := s.dir.(directory.UserPrefsStore)
	return store, ok
}

// setPrefsCookie caches one preference for the panel; an empty value clears it.
func setPrefsCookie(w http.ResponseWriter, name, value string) {
	maxAge := prefsCookieMaxAge
	if value == "" {
		maxAge = -1
	}
	// #nosec G124 -- theme.js reads the theme cookie before the first paint, so it cannot be HttpOnly; both cookies hold only a preference name
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/admin", MaxAge: maxAge,
		Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// syncPrefs wraps the panel so every full page load renders in the caller's
// stored language and carries the stored theme and language in their cookies, so
// a choice made in webmail or on another browser shows here from the first paint.
// A panel fragment (an htmx request) is left alone: it renders in the language
// the cookie holds, which the page around it already brought in line.
func (s *Server) syncPrefs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/ui/") && r.Header.Get("HX-Request") == "" {
			r = s.applyStoredPrefs(w, r)
		}
		next.ServeHTTP(w, r)
	})
}

// applyStoredPrefs brings the theme, time zone and language cookies in line with
// the signed-in caller's users record and returns the request carrying the stored
// language and time zone. A caller who never chose a theme keeps whatever the browser holds; a
// caller who never chose a language follows the browser's.
func (s *Server) applyStoredPrefs(w http.ResponseWriter, r *http.Request) *http.Request {
	p, ok := s.storedPrefs(r)
	if !ok {
		return r
	}
	if p.Theme != "" && requestCookie(r, themeCookie) != p.Theme {
		setPrefsCookie(w, themeCookie, p.Theme)
	}
	r = syncZoneCookie(w, r, p.Timezone)
	lang := p.Lang
	if !supportedLang(lang) {
		lang = ""
	}
	if requestCookie(r, langCookie) != lang {
		setPrefsCookie(w, langCookie, lang)
	}
	if lang == "" {
		// The cookie this request carried is stale now, so it must not decide.
		return withLang(r, acceptLanguage(r.Header.Get("Accept-Language")))
	}
	return withLang(r, lang)
}

// storedPrefs reads the signed-in caller's users record; ok is false for a
// request without a session, a directory without preferences, or a failed read,
// which is recorded.
func (s *Server) storedPrefs(r *http.Request) (directory.UserPrefs, bool) {
	cl, ok := s.uiClaims(r)
	if !ok {
		return directory.UserPrefs{}, false
	}
	store, ok := s.prefsStore()
	if !ok {
		return directory.UserPrefs{}, false
	}
	p, found, err := store.GetUserPrefs(cl.Login)
	if err != nil {
		s.logger.Emit(logging.Event{
			Level: logging.LevelError, Subsystem: logging.Admin, Name: "prefs.read_fail", Err: err.Error(),
		})
		return directory.UserPrefs{}, false
	}
	return p, found
}

// adoptSignInPrefs stores the theme and language the sign-in page cached in its
// cookies, before any session could store them, where the users record holds no
// choice yet. A choice the record holds wins. A failure is recorded and does not
// stop the sign-in.
func (s *Server) adoptSignInPrefs(r *http.Request, login string) {
	store, ok := s.prefsStore()
	if !ok {
		return
	}
	p, found, err := store.GetUserPrefs(login)
	if err == nil && found {
		u := signInAdoptions(r, p)
		if u.Theme == nil && u.Lang == nil {
			return
		}
		_, err = store.SetUserPrefs(login, u)
	}
	if err != nil {
		s.logger.Emit(logging.Event{
			Level: logging.LevelError, Subsystem: logging.Admin, Name: "prefs.adopt_fail", Err: err.Error(),
		})
	}
}

// signInAdoptions lists the cookie choices the users record p does not hold yet;
// a cookie value that is no valid choice is left out.
func signInAdoptions(r *http.Request, p directory.UserPrefs) directory.UserPrefsUpdate {
	var u directory.UserPrefsUpdate
	if theme := requestCookie(r, themeCookie); p.Theme == "" && theme != "" && directory.ValidPrefs(directory.UserPrefsUpdate{Theme: &theme}) {
		u.Theme = &theme
	}
	if lang := requestCookie(r, langCookie); p.Lang == "" && supportedLang(lang) {
		u.Lang = &lang
	}
	return u
}

// requestCookie returns the named cookie's value, or "" when the request has none.
func requestCookie(r *http.Request, name string) string {
	if c, err := r.Cookie(name); err == nil {
		return c.Value
	}
	return ""
}

// prefsUpdate reads the theme and language a save names; a field the form does
// not carry keeps its stored value.
func prefsUpdate(r *http.Request) directory.UserPrefsUpdate {
	var u directory.UserPrefsUpdate
	if err := r.ParseForm(); err != nil {
		return u
	}
	if v, ok := r.PostForm["theme"]; ok && len(v) > 0 {
		u.Theme = &v[0]
	}
	if v, ok := r.PostForm["lang"]; ok && len(v) > 0 {
		u.Lang = &v[0]
	}
	return u
}

// handleUISavePrefs stores the signed-in operator's theme or language in the users
// record webmail shares. Any panel user may set their own, so it asks for a
// session and the CSRF header only.
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
	u := prefsUpdate(r)
	if u.Theme == nil && u.Lang == nil {
		http.Error(w, "no preference named", http.StatusBadRequest)
		return
	}
	found, err := store.SetUserPrefs(cl.Login, u)
	s.answerPrefsSave(w, u, found, err)
}

// answerPrefsSave answers a preference save and, when it was stored, caches the
// saved values in their cookies.
func (s *Server) answerPrefsSave(w http.ResponseWriter, u directory.UserPrefsUpdate, found bool, err error) {
	switch {
	case errors.Is(err, directory.ErrInvalidPref):
		http.Error(w, "invalid preference", http.StatusBadRequest)
	case err != nil:
		s.fail(w, "could not save the preference", err, http.StatusInternalServerError)
	case !found:
		http.Error(w, "no such user", http.StatusNotFound)
	default:
		if u.Theme != nil {
			setPrefsCookie(w, themeCookie, *u.Theme)
		}
		if u.Lang != nil {
			setPrefsCookie(w, langCookie, *u.Lang)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleUISaveTimezone stores the signed-in operator's time zone in the users
// record webmail shares, and answers the account page's result line. The account
// is the session's, never the form's.
func (s *Server) handleUISaveTimezone(w http.ResponseWriter, r *http.Request) {
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
	zone := strings.TrimSpace(r.FormValue("timezone"))
	found, err := store.SetUserPrefs(cl.Login, directory.UserPrefsUpdate{Timezone: &zone})
	s.render(w, r, "change-password-result", s.timezoneSaveResult(w, zone, found, err))
}

// timezoneSaveResult is the result line of a time zone save; a stored zone is
// also cached in its cookie, so the fragments render in it at once.
func (s *Server) timezoneSaveResult(w http.ResponseWriter, zone string, found bool, err error) map[string]any {
	switch {
	case errors.Is(err, directory.ErrInvalidPref):
		return map[string]any{"OK": false, "Message": "account.zoneInvalid"}
	case err != nil:
		return map[string]any{"OK": false, "Message": s.notice("account.zoneFailed", err)}
	case !found:
		return map[string]any{"OK": false, "Message": "account.zoneFailed"}
	}
	setPrefsCookie(w, zoneCookie, zone)
	if zone == "" {
		return map[string]any{"OK": true, "Message": "account.zoneClearedUTC"}
	}
	return map[string]any{"OK": true, "Message": msg("account.zoneSaved", zone)}
}
