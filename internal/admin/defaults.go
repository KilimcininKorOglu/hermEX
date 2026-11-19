package admin

import (
	"encoding/json"
	"errors"
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
		v := int64(0)
		if s := strings.TrimSpace(r.PostFormValue(name)); s != "" {
			v = quotaKB(s)
		}
		return &v
	}
	lang := r.PostFormValue("lang")
	return directory.UserCreateDefaults{
		Lang: &lang, POP3IMAP: b("pop3_imap"), SMTP: b("smtp"), ChgPasswd: b("chgpasswd"),
		Web: b("web"), EAS: b("eas"), DAV: b("dav"),
		StorageKB: mb("storagemb"), ReceiveKB: mb("receivemb"), SendKB: mb("sendmb"),
	}
}

// quotaKB reads a quota entered in MiB as KiB. A value that is not a number, is
// negative, or overflows when scaled reads as -1, which quotasValid refuses: before,
// a typo was stored as 0, the unlimited quota, and a large count wrapped.
func quotaKB(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || !fitsInBytes(n, 10) {
		return -1
	}
	return n * 1024
}

// quotasValid reports whether every quota a defaults set carries is unset or a
// non-negative size. It guards the form and the JSON API alike.
func quotasValid(u directory.UserCreateDefaults) bool {
	for _, q := range []*int64{u.StorageKB, u.ReceiveKB, u.SendKB} {
		if q != nil && *q < 0 {
			return false
		}
	}
	return true
}

// createDefaultsFromForm builds the full system create-defaults from the editor.
func createDefaultsFromForm(r *http.Request) directory.CreateDefaults {
	// A value that is not a number reads as -1, which maxUserValid refuses: read as
	// 0 it made "no limit" the default for every new domain.
	return directory.CreateDefaults{
		Domain: directory.DomainCreateDefaults{MaxUser: int64(formInt(r, "maxUser"))},
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
		kb := quotaKB(v)
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
		s.fail(w, "server error", derr, http.StatusInternalServerError)
		return
	} else if !found {
		http.Error(w, "no such domain", http.StatusNotFound)
		return
	}
	cd, _, err := s.dir.GetCreateDefaults(id)
	if err != nil {
		s.fail(w, "could not read create defaults", err, http.StatusInternalServerError)
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
		s.fail(w, "server error", derr, http.StatusInternalServerError)
		return
	} else if !found {
		http.Error(w, "no such domain", http.StatusNotFound)
		return
	}
	var u directory.UserCreateDefaults
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil || !quotasValid(u) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.storeDomainOverride(id, u); err != nil {
		s.fail(w, "could not save create defaults", err, http.StatusInternalServerError)
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
	u := userOverrideFromForm(r)
	if err != nil {
		data["Error"] = "domains.invalidID"
	} else if !quotasValid(u) {
		data["Error"] = "userDetail.quotaInvalid"
	} else if err := s.storeDomainOverride(id, u); err != nil {
		data["Error"] = s.notice("userDetail.saveFailed", err)
	} else {
		data["Saved"] = true
	}
	s.render(w, r, "user-status", data)
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
		s.fail(w, "could not read create defaults", err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, cd)
}

// handleSetDefaults replaces the system-wide create-defaults (system administrators).
func (s *Server) handleSetDefaults(w http.ResponseWriter, r *http.Request) {
	var cd directory.CreateDefaults
	if err := json.NewDecoder(r.Body).Decode(&cd); err != nil || !quotasValid(cd.User) || cd.Domain.MaxUser < 0 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.dir.SetCreateDefaults(0, cd); err != nil {
		s.fail(w, "could not save create defaults", err, http.StatusInternalServerError)
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
	cd, _, cdErr := s.dir.GetCreateDefaults(0)
	rd, rdErr := s.dir.EffectiveUserDefaults(0)
	failed := readFailures{}
	data := map[string]any{"Nav": "defaults", "CSRF": csrfCookieValue(r), "ReadFailed": failed}
	if s.noteRead(failed, "defaults", "what.createDefaults", errors.Join(cdErr, rdErr)) {
		data["MaxUser"] = cd.Domain.MaxUser
		data["Fields"] = userCreateFieldsOf(rd)
	}
	s.render(w, r, "defaults.html", data)
}

// handleUISaveDefaults saves the system create-defaults from the editor and returns
// the refreshed status panel.
func (s *Server) handleUISaveDefaults(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	data := map[string]any{}
	cd := createDefaultsFromForm(r)
	if !quotasValid(cd.User) {
		data["Error"] = "userDetail.quotaInvalid"
	} else if cd.Domain.MaxUser < 0 {
		data["Error"] = "domains.maxUserInvalid"
	} else if err := s.dir.SetCreateDefaults(0, cd); err != nil {
		data["Error"] = s.notice("userDetail.saveFailed", err)
	} else {
		data["Saved"] = true
	}
	s.render(w, r, "user-status", data)
}
