package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"hermex/internal/directory"
)

// userDefaultsFromForm builds a full user create-defaults set from the editor form.
// Every field is set, since the system editor defines the base layer; quotas are
// read in MiB and stored in KiB.
func userDefaultsFromForm(r *http.Request) directory.UserCreateDefaults {
	b := func(name string) *bool { v := r.PostFormValue(name) != ""; return &v }
	mb := func(name string) *int64 {
		n, _ := strconv.ParseInt(r.PostFormValue(name), 10, 64)
		v := n * 1024
		return &v
	}
	lang := r.PostFormValue("lang")
	return directory.UserCreateDefaults{
		Lang: &lang, POP3IMAP: b("pop3_imap"), SMTP: b("smtp"), ChgPasswd: b("chgpasswd"),
		Web: b("web"), EAS: b("eas"), DAV: b("dav"),
		StorageKB: mb("storagemb"), ReceiveKB: mb("receivemb"), SendKB: mb("sendmb"),
	}
}

// createDefaultsFromForm builds the full system create-defaults from the editor.
func createDefaultsFromForm(r *http.Request) directory.CreateDefaults {
	maxUser, _ := strconv.ParseInt(r.PostFormValue("maxUser"), 10, 64)
	return directory.CreateDefaults{
		Domain: directory.DomainCreateDefaults{MaxUser: maxUser},
		User:   userDefaultsFromForm(r),
	}
}

// handleGetDefaults returns the system-wide create-defaults (system administrators).
func (s *Server) handleGetDefaults(w http.ResponseWriter, r *http.Request) {
	cd, _, err := s.dir.GetCreateDefaults(0)
	if err != nil {
		http.Error(w, "could not read create defaults", http.StatusInternalServerError)
		return
	}
	writeJSON(w, cd)
}

// handleSetDefaults replaces the system-wide create-defaults (system administrators).
func (s *Server) handleSetDefaults(w http.ResponseWriter, r *http.Request) {
	var cd directory.CreateDefaults
	if err := json.NewDecoder(r.Body).Decode(&cd); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.dir.SetCreateDefaults(0, cd); err != nil {
		http.Error(w, "could not save create defaults: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUIDefaults renders the system create-defaults editor page, its user
// section pre-filled with the effective (resolved) values.
func (s *Server) handleUIDefaults(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	cd, _, _ := s.dir.GetCreateDefaults(0)
	rd, _ := s.dir.EffectiveUserDefaults(0)
	s.render(w, "defaults.html", map[string]any{
		"Nav":     "defaults",
		"CSRF":    csrfCookieValue(r),
		"MaxUser": cd.Domain.MaxUser,
		"Fields":  userCreateFieldsOf(rd),
	})
}

// handleUISaveDefaults saves the system create-defaults from the editor and returns
// the refreshed status panel.
func (s *Server) handleUISaveDefaults(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	data := map[string]any{}
	if err := s.dir.SetCreateDefaults(0, createDefaultsFromForm(r)); err != nil {
		data["Error"] = "Could not save: " + err.Error()
	} else {
		data["Saved"] = true
	}
	s.render(w, "user-status", data)
}
