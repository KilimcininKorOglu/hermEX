package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

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

// userOverrideView renders a per-domain user-defaults override as tri-state form
// controls: a toggle is "" (inherit), "1" (on) or "0" (off); a text/number field
// is "" (inherit) or its value (quotas in MiB).
type userOverrideView struct {
	Lang      string
	POP3IMAP  string
	SMTP      string
	ChgPasswd string
	Web       string
	EAS       string
	DAV       string
	StorageMB string
	ReceiveMB string
	SendMB    string
}

// userOverrideViewOf projects a stored override (pointer-per-field) onto the
// tri-state form model: a nil field shows as inherit.
func userOverrideViewOf(u directory.UserCreateDefaults) userOverrideView {
	tri := func(p *bool) string {
		if p == nil {
			return ""
		}
		if *p {
			return "1"
		}
		return "0"
	}
	mb := func(p *int64) string {
		if p == nil {
			return ""
		}
		return strconv.FormatInt(*p/1024, 10)
	}
	v := userOverrideView{
		POP3IMAP: tri(u.POP3IMAP), SMTP: tri(u.SMTP), ChgPasswd: tri(u.ChgPasswd),
		Web: tri(u.Web), EAS: tri(u.EAS), DAV: tri(u.DAV),
		StorageMB: mb(u.StorageKB), ReceiveMB: mb(u.ReceiveKB), SendMB: mb(u.SendKB),
	}
	if u.Lang != nil {
		v.Lang = *u.Lang
	}
	return v
}

// userOverrideFromForm parses the tri-state override form: a blank control means
// inherit (nil), anything else sets the field. Quotas are MiB → KiB.
func userOverrideFromForm(r *http.Request) directory.UserCreateDefaults {
	triP := func(name string) *bool {
		switch r.PostFormValue(name) {
		case "1":
			v := true
			return &v
		case "0":
			v := false
			return &v
		default:
			return nil
		}
	}
	mbP := func(name string) *int64 {
		v := strings.TrimSpace(r.PostFormValue(name))
		if v == "" {
			return nil
		}
		n, _ := strconv.ParseInt(v, 10, 64)
		kb := n * 1024
		return &kb
	}
	var langP *string
	if l := strings.TrimSpace(r.PostFormValue("lang")); l != "" {
		langP = &l
	}
	return directory.UserCreateDefaults{
		Lang: langP, POP3IMAP: triP("pop3_imap"), SMTP: triP("smtp"), ChgPasswd: triP("chgpasswd"),
		Web: triP("web"), EAS: triP("eas"), DAV: triP("dav"),
		StorageKB: mbP("storagemb"), ReceiveKB: mbP("receivemb"), SendKB: mbP("sendmb"),
	}
}

// userOverrideEmpty reports whether an override sets nothing (every field inherits),
// in which case the per-domain row is removed rather than stored empty.
func userOverrideEmpty(u directory.UserCreateDefaults) bool {
	return u.Lang == nil && u.POP3IMAP == nil && u.SMTP == nil && u.ChgPasswd == nil &&
		u.Web == nil && u.EAS == nil && u.DAV == nil &&
		u.StorageKB == nil && u.ReceiveKB == nil && u.SendKB == nil
}

// handleGetDomainDefaults returns a domain's per-domain user-defaults override.
func (s *Server) handleGetDomainDefaults(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	if _, found, derr := s.dir.GetDomain(id); derr != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	} else if !found {
		http.Error(w, "no such domain", http.StatusNotFound)
		return
	}
	cd, _, err := s.dir.GetCreateDefaults(id)
	if err != nil {
		http.Error(w, "could not read create defaults", http.StatusInternalServerError)
		return
	}
	writeJSON(w, cd.User)
}

// handleSetDomainDefaults replaces a domain's per-domain user-defaults override; an
// override that sets nothing clears the row so the domain falls back to the system
// defaults.
func (s *Server) handleSetDomainDefaults(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	if _, found, derr := s.dir.GetDomain(id); derr != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	} else if !found {
		http.Error(w, "no such domain", http.StatusNotFound)
		return
	}
	var u directory.UserCreateDefaults
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.storeDomainOverride(id, u); err != nil {
		http.Error(w, "could not save create defaults: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUISaveDomainDefaults saves a domain's per-domain override from the detail
// form and returns the refreshed status panel.
func (s *Server) handleUISaveDomainDefaults(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	data := map[string]any{}
	if err != nil {
		data["Error"] = "Invalid domain id."
	} else if err := s.storeDomainOverride(id, userOverrideFromForm(r)); err != nil {
		data["Error"] = "Could not save: " + err.Error()
	} else {
		data["Saved"] = true
	}
	s.render(w, "user-status", data)
}

// storeDomainOverride writes a domain's user-defaults override, or clears the row
// when the override sets nothing.
func (s *Server) storeDomainOverride(id int64, u directory.UserCreateDefaults) error {
	if userOverrideEmpty(u) {
		_, err := s.dir.DeleteCreateDefaults(id)
		return err
	}
	return s.dir.SetCreateDefaults(id, directory.CreateDefaults{User: u})
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
