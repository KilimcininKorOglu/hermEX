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

// handleUISyncLDAP enqueues the directory downsync as an async task and returns
// the panel acknowledging it. A directory sync can be long-running, so it runs on
// the task worker rather than blocking the request; its result appears on the Task
// queue page. The actual sync is performLDAPSync, shared with the worker.
func (s *Server) handleUISyncLDAP(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiAuthorized(w, r)
	if !ok {
		return
	}
	if s.syncer == nil {
		s.render(w, "ldap-panel", s.ldapPanelData(r, false, "", "Directory sync is not available."))
		return
	}
	id, err := s.dir.CreateTask("ldapsync", "", cl.Login)
	if err != nil {
		s.render(w, "ldap-panel", s.ldapPanelData(r, false, "", "Could not queue the sync: "+err.Error()))
		return
	}
	s.render(w, "ldap-panel", s.ldapPanelData(r, false,
		fmt.Sprintf("Directory sync queued as task #%d — watch the Task queue for its result.", id), ""))
}
