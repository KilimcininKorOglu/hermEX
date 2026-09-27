package webmail2api

import (
	"errors"
	"net/http"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// prefsUpdateJSON is a PUT /account/prefs body; an absent field keeps its value.
type prefsUpdateJSON struct {
	Theme             *string `json:"theme"`
	Locale            *string `json:"locale"`
	ShowWelcomeBanner *bool   `json:"show_welcome_banner"`
}

// prefsFields renders stored preferences under the keys /auth/me and
// /account/prefs both answer with.
func prefsFields(p directory.UserPrefs) map[string]any {
	return map[string]any{"theme": p.Theme, "locale": p.Lang, "show_welcome_banner": p.ShowWelcome}
}

// callerPrefs reads the caller's interface preferences, reporting false when the
// directory cannot store them or the read failed. A failed read is recorded, since
// the caller then falls back to its cookies and nothing else shows the fault.
func (s *Server) callerPrefs(email string) (directory.UserPrefs, bool) {
	store, ok := s.auth.(directory.UserPrefsStore)
	if !ok {
		return directory.UserPrefs{}, false
	}
	p, found, err := store.GetUserPrefs(email)
	if err != nil {
		logError("read-prefs", err, logging.Fields{"user": email})
		return directory.UserPrefs{}, false
	}
	return p, found
}

// handlePutPrefs stores the caller's interface preferences (theme, language and
// the welcome banner), the record the admin panel shares, and answers with the
// stored result.
func (s *Server) handlePutPrefs(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	store, ok := s.auth.(directory.UserPrefsStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "preferences are not supported"})
		return
	}
	var body prefsUpdateJSON
	if err := decodeJSON(r, &body); err != nil || (body.Theme == nil && body.Locale == nil && body.ShowWelcomeBanner == nil) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	found, err := store.SetUserPrefs(c.Email, directory.UserPrefsUpdate{
		Theme: body.Theme, Lang: body.Locale, ShowWelcome: body.ShowWelcomeBanner,
	})
	switch {
	case errors.Is(err, directory.ErrInvalidPref):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid preference"})
	case err != nil:
		logError("write-prefs", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the preferences"})
	case !found:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such user"})
	default:
		p, _ := s.callerPrefs(c.Email)
		writeJSON(w, http.StatusOK, prefsFields(p))
	}
}
