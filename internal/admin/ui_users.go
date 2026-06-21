package admin

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/directory"
)

// uiAuthorized authorizes a UI state-changing request: a valid session, a
// matching CSRF header (the htmx double-submit), and full system authority. A
// read-only system administrator is refused here — they may view pages but not
// change state. On failure it writes an error response and returns ok=false.
func (s *Server) uiAuthorized(w http.ResponseWriter, r *http.Request) (claims, bool) {
	cl, ok := s.uiClaims(r)
	if !ok {
		http.Error(w, "session expired", http.StatusUnauthorized)
		return claims{}, false
	}
	if !validCSRF(r) {
		http.Error(w, "missing or invalid CSRF token", http.StatusForbidden)
		return claims{}, false
	}
	if !s.isSystemAdmin(cl.UserID) {
		http.Error(w, "forbidden: requires a full system administrator", http.StatusForbidden)
		return claims{}, false
	}
	return cl, true
}

// handleUIUsers renders the users management page (system administrators only).
func (s *Server) handleUIUsers(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	users, _ := s.dir.ListUsers()
	s.render(w, "users.html", map[string]any{
		"Nav":   "users",
		"CSRF":  csrfCookieValue(r),
		"Users": users,
	})
}

// handleUICreateUser creates a user from the management form and returns the
// refreshed users panel for htmx to swap in; a validation or directory error is
// reported in the panel rather than failing the request.
func (s *Server) handleUICreateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	email := r.PostFormValue("email")
	var errMsg string
	switch {
	case email == "" || r.PostFormValue("password") == "":
		errMsg = "An email and password are required."
	default:
		if _, err := s.dir.CreateUser(email, r.PostFormValue("password"), s.paths.MaildirFor(email)); err != nil {
			errMsg = "Could not create user: " + err.Error()
		}
	}
	users, _ := s.dir.ListUsers()
	s.render(w, "users-panel", map[string]any{"Users": users, "Error": errMsg})
}

// handleUIUserDetail renders one user's detail/edit page (system administrators
// only). The user is named in the path.
func (s *Server) handleUIUserDetail(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	altnames, _ := s.dir.ListAltnames(u.Username)
	aliases, _ := s.dir.ListAliasesFor(u.Username)
	roles, _ := s.dir.AdminRoles(u.ID)
	props, _ := s.dir.GetUserProperties(u.Username)
	oof, _ := s.store.GetOOFSettings(u.Maildir)
	devs, _ := s.store.ListDevices(u.Maildir)
	qlimits, qused, _ := s.store.GetQuota(u.Maildir)
	delegates, _ := s.store.GetDelegates(u.Maildir)
	sendAs, _ := s.store.GetSendAs(u.Maildir)
	storeOwners, _ := s.store.GetStoreOwners(u.Maildir)
	meetingCfg, _ := s.store.GetMeetingConfig(u.Maildir)
	syncPol, _ := s.store.GetSyncPolicy(u.Maildir)
	fmEntries, _ := s.dir.ListFetchmail(u.Username)
	folders, _ := s.store.ListFolders(u.Maildir)
	s.render(w, "user_detail.html", map[string]any{
		"Nav":         "users",
		"CSRF":        csrfCookieValue(r),
		"User":        u,
		"Email":       u.Username,
		"Altnames":    strings.Join(altnames, "\n"),
		"Aliases":     strings.Join(aliases, "\n"),
		"Forward":     s.forwardViewOf(u.Username),
		"Roles":       roles,
		"Contact":     contactValues(props),
		"OOF":         oofViewOf(oof),
		"Devices":     deviceViewsOf(devs),
		"Quota":       quotaViewOf(qlimits, qused),
		"Hide":        hideViewOf(props),
		"Delegates":   strings.Join(delegates, "\n"),
		"SendAs":      strings.Join(sendAs, "\n"),
		"StoreOwners": strings.Join(storeOwners, "\n"),
		"Meeting":     meetingCfg,
		"SyncPolicy":  policyView(syncPol),
		"Fetchmail":   fetchmailViews(fmEntries),
		"Folders":     folders,
	})
}

// handleUIUserContact saves the user's contact/detail fields from the form and
// returns the refreshed status panel; an empty field clears that property.
func (s *Server) handleUIUserContact(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	in := map[string]string{}
	for _, f := range contactFields {
		in[f.Field] = r.PostFormValue(f.Field)
	}
	found, err := s.dir.SetUserProperties(r.PathValue("email"), contactProps(in))
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = "Could not save contact: " + err.Error()
	case !found:
		data["Error"] = "No such user."
	default:
		data["Saved"] = true
	}
	s.render(w, "user-status", data)
}

// renderUserRoles re-renders the admin-roles panel for htmx after a grant or
// revoke, carrying an optional error message.
func (s *Server) renderUserRoles(w http.ResponseWriter, email, csrf string, uid int64, errMsg string) {
	roles, err := s.dir.AdminRoles(uid)
	if err != nil && errMsg == "" {
		errMsg = "Could not load roles: " + err.Error()
	}
	s.render(w, "user-roles", map[string]any{
		"Email": email,
		"CSRF":  csrf,
		"Roles": roles,
		"Error": errMsg,
	})
}

// handleUIUserGrantRole grants the user an admin role from the detail form and
// returns the refreshed roles panel.
func (s *Server) handleUIUserGrantRole(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	uid, ok := s.resolveUser(w, r)
	if !ok {
		return
	}
	scopeID, _ := strconv.ParseInt(r.PostFormValue("scopeID"), 10, 64)
	errMsg := ""
	if err := s.dir.GrantAdminRole(uid, r.PostFormValue("role"), scopeID); err != nil {
		errMsg = "Could not grant role: " + err.Error()
	}
	s.renderUserRoles(w, r.PathValue("email"), csrfCookieValue(r), uid, errMsg)
}

// handleUIUserRevokeRole removes one of the user's admin roles and returns the
// refreshed roles panel.
func (s *Server) handleUIUserRevokeRole(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	uid, ok := s.resolveUser(w, r)
	if !ok {
		return
	}
	scopeID, _ := strconv.ParseInt(r.PostFormValue("scopeID"), 10, 64)
	errMsg := ""
	if err := s.dir.RevokeAdminRole(uid, r.PostFormValue("role"), scopeID); err != nil {
		errMsg = "Could not revoke role: " + err.Error()
	}
	s.renderUserRoles(w, r.PathValue("email"), csrfCookieValue(r), uid, errMsg)
}

// handleUIUserAliases replaces the user's e-mail aliases from the textarea
// (whitespace-separated) and returns the refreshed status panel; an address
// already in use is reported there.
func (s *Server) handleUIUserAliases(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	found, err := s.dir.SetAliasesFor(r.PathValue("email"), strings.Fields(r.PostFormValue("aliases")))
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = "Could not save aliases: " + err.Error()
	case !found:
		data["Error"] = "No such user."
	default:
		data["Saved"] = true
	}
	s.render(w, "user-status", data)
}

// handleUIUserAltnames replaces the user's alternative login names from the
// textarea (whitespace-separated) and returns the refreshed status panel; a name
// already taken by another account is reported there.
func (s *Server) handleUIUserAltnames(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	found, err := s.dir.SetAltnames(r.PathValue("email"), strings.Fields(r.PostFormValue("altnames")))
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = "Could not save alternative names: " + err.Error()
	case !found:
		data["Error"] = "No such user."
	default:
		data["Saved"] = true
	}
	s.render(w, "user-status", data)
}

// handleUIUserEdit saves the edited account fields and returns the refreshed
// status panel for htmx to swap in; a directory error is reported in the panel
// rather than failing the request.
func (s *Server) handleUIUserEdit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	atoi := func(v string) int { n, _ := strconv.Atoi(v); return n }
	found, err := s.dir.UpdateUser(r.PathValue("email"), directory.UserUpdate{
		Status:      atoi(r.PostFormValue("status")),
		Lang:        r.PostFormValue("lang"),
		Timezone:    r.PostFormValue("timezone"),
		DisplayType: atoi(r.PostFormValue("displayType")),
		Homeserver:  atoi(r.PostFormValue("homeserver")),
		POP3IMAP:    r.PostFormValue("pop3_imap") != "",
		SMTP:        r.PostFormValue("smtp") != "",
		ChgPasswd:   r.PostFormValue("chgpasswd") != "",
		Web:         r.PostFormValue("web") != "",
		EAS:         r.PostFormValue("eas") != "",
		DAV:         r.PostFormValue("dav") != "",
	})
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = "Could not save: " + err.Error()
	case !found:
		data["Error"] = "No such user."
	default:
		data["Saved"] = true
	}
	s.render(w, "user-status", data)
}

// handleUIUserDelete deletes the user and redirects the browser back to the user
// list via htmx. The mailbox files are removed only when the deleteFiles checkbox
// is set.
func (s *Server) handleUIUserDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	deleteFiles := r.PostFormValue("deleteFiles") != ""
	if _, err := s.dir.DeleteUser(r.PathValue("email"), deleteFiles); err != nil {
		http.Error(w, "could not delete user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/admin/ui/users")
	w.WriteHeader(http.StatusOK)
}
