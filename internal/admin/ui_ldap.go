package admin

import (
	"fmt"
	"net/http"

	"hermex/internal/directory"
)

// defaultOrgID is the organization the Directory Sync page manages. The
// directory is single-organization in practice (domains.org_id defaults to 0),
// so the UI manages org 0; per-org configuration remains available over the JSON
// API.
const defaultOrgID = 0

// ldapPanelData builds the Directory Sync panel data: the stored config with its
// bind password stripped (never sent to the browser) plus a flag noting whether
// one is set, and an optional saved/sync/error message.
func (s *Server) ldapPanelData(r *http.Request, saved bool, syncResult, errMsg string) map[string]any {
	cfg, ok, _ := s.dir.GetLDAPConfig(defaultOrgID)
	bindSet := ok && cfg.BindPassword != ""
	cfg.BindPassword = "" // never echo the secret back to the browser
	data := map[string]any{
		"Nav":             "ldap",
		"CSRF":            csrfCookieValue(r),
		"Config":          cfg,
		"BindPasswordSet": bindSet,
		"Saved":           saved,
	}
	if syncResult != "" {
		data["SyncResult"] = syncResult
	}
	if errMsg != "" {
		data["Error"] = errMsg
	}
	return data
}

// handleUILDAP renders the Directory Sync page (system administrators only).
func (s *Server) handleUILDAP(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "ldap.html", s.ldapPanelData(r, false, "", ""))
}

// handleUISaveLDAP stores the directory configuration and returns the refreshed
// panel. An empty bind password preserves the stored secret.
func (s *Server) handleUISaveLDAP(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	existing, _, _ := s.dir.GetLDAPConfig(defaultOrgID)
	cfg := directory.LDAPConfig{
		URI:          r.PostFormValue("uri"),
		StartTLS:     r.PostFormValue("starttls") != "",
		BindDN:       r.PostFormValue("bind_dn"),
		BindPassword: r.PostFormValue("bind_password"),
		BaseDN:       r.PostFormValue("base_dn"),
		UsernameAttr: r.PostFormValue("username_attr"),
	}
	if cfg.BindPassword == "" {
		cfg.BindPassword = existing.BindPassword
	}
	if err := s.dir.SetLDAPConfig(defaultOrgID, cfg); err != nil {
		s.render(w, "ldap-panel", s.ldapPanelData(r, false, "", "Could not save the configuration: "+err.Error()))
		return
	}
	s.render(w, "ldap-panel", s.ldapPanelData(r, true, "", ""))
}

// handleUISyncLDAP runs the directory downsync for the default org and returns
// the panel with the import counts. A user whose mail domain is not provisioned
// locally is skipped rather than failing the whole sync.
func (s *Server) handleUISyncLDAP(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	if s.syncer == nil {
		s.render(w, "ldap-panel", s.ldapPanelData(r, false, "", "Directory sync is not available."))
		return
	}
	cfg, ok, err := s.dir.GetLDAPConfig(defaultOrgID)
	if err != nil || !ok {
		s.render(w, "ldap-panel", s.ldapPanelData(r, false, "", "No directory is configured yet."))
		return
	}
	users, err := s.syncer.Sync(cfg)
	if err != nil {
		s.render(w, "ldap-panel", s.ldapPanelData(r, false, "", "Sync failed: "+err.Error()))
		return
	}
	var created, updated int
	for _, u := range users {
		isNew, err := s.dir.UpsertLDAPUser(u.Username, u.ExternID, s.paths.MaildirFor(u.Username))
		if err != nil {
			continue
		}
		if isNew {
			created++
		} else {
			updated++
		}
	}
	msg := fmt.Sprintf("Synced %d directory entries: %d created, %d updated.", len(users), created, updated)
	s.render(w, "ldap-panel", s.ldapPanelData(r, false, msg, ""))
}
