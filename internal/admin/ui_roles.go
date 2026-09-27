package admin

import (
	"net/http"
	"strconv"

	"hermex/internal/directory"
)

// capCheck is one unscoped capability checkbox in the role editor.
type capCheck struct {
	Name    string
	Label   string
	Checked bool
}

// scopeItem is one selectable scope for a scoped permission: a specific org or
// domain, or "*" for all of them.
type scopeItem struct {
	ID      string
	Label   string
	Checked bool
}

// userCheck is one user the role may be assigned to.
type userCheck struct {
	ID       int64
	Label    string
	Assigned bool
}

// handleUIRoles renders the roles list page. A read-only admin may view it.
func (s *Server) handleUIRoles(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "roles.html", s.rolesPanelData(r, ""))
}

// rolesPanelData assembles the roles list and panel state.
func (s *Server) rolesPanelData(r *http.Request, errMsg string) map[string]any {
	roles, err := s.dir.ListRoles()
	return map[string]any{
		"Nav":        "roles",
		"CSRF":       csrfCookieValue(r),
		"Roles":      roles,
		"RolesError": s.listFailure("the roles", err),
		"Error":      errMsg,
	}
}

// handleUICreateRole creates a role from the list-page form (name and
// description) and returns the refreshed panel; permissions and users are then
// configured on the role's detail page.
func (s *Server) handleUICreateRole(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	var errMsg string
	if _, err := s.dir.CreateRole(r.PostFormValue("name"), r.PostFormValue("description"), nil, nil); err != nil {
		errMsg = s.notice("Could not create role.", err)
	}
	s.render(w, "roles-panel", s.rolesPanelData(r, errMsg))
}

// handleUIRoleDetail renders one role's editor: identity, the permission
// checkboxes (unscoped capabilities plus per-org and per-domain scoped grants),
// and the user assignment. A read-only admin may view it.
func (s *Server) handleUIRoleDetail(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	id, ok := roleIDParam(w, r)
	if !ok {
		return
	}
	role, found, err := s.dir.GetRole(id)
	if err != nil {
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such role", http.StatusNotFound)
		return
	}
	s.render(w, "role_detail.html", s.roleDetailData(r, role, nil, "", false))
}

// roleDetailData builds the role editor's view model. The save replaces the role's
// whole permission set and user list with what the form checks, so the editor is
// hidden when the role or any list it offers could not be read: an organization,
// domain or user missing from the form would lose its grant on the next save.
func (s *Server) roleDetailData(r *http.Request, role directory.RoleDetail, roleErr error, errMsg string, saved bool) map[string]any {
	failed := readFailures{}
	data := map[string]any{
		"Nav": "roles", "CSRF": csrfCookieValue(r), "Role": role,
		"Error": errMsg, "Saved": saved, "ReadFailed": failed,
	}
	orgs, orgErr := s.dir.ListOrgs()
	domains, domErr := s.dir.ListDomains()
	users, userErr := s.dir.ListUsers()
	if s.noteRead(failed, "editor", "the role", roleErr) &&
		s.noteRead(failed, "editor", "the organizations", orgErr) &&
		s.noteRead(failed, "editor", "the domains", domErr) &&
		s.noteRead(failed, "editor", "the users", userErr) {
		addRoleChecks(data, role, orgs, domains, users)
	}
	return data
}

// addRoleChecks adds the role editor's checkboxes to its view model with each
// checked state precomputed, so the template only renders.
func addRoleChecks(data map[string]any, role directory.RoleDetail, orgs []directory.OrgInfo, domains []directory.DomainInfo, users []directory.UserInfo) {
	has := func(name, params string) bool {
		for _, p := range role.Permissions {
			if p.Name == name && p.Params == params {
				return true
			}
		}
		return false
	}
	caps := []capCheck{
		{directory.PermSystemAdmin, "System administrator (full)", has(directory.PermSystemAdmin, "")},
		{directory.PermSystemAdminRO, "System administrator (read-only)", has(directory.PermSystemAdminRO, "")},
		{directory.PermDomainPurge, "Purge domains", has(directory.PermDomainPurge, "")},
		{directory.PermResetPasswd, "Reset user passwords", has(directory.PermResetPasswd, "")},
	}
	orgScopes := []scopeItem{{ID: "*", Label: "All organizations", Checked: has(directory.PermOrgAdmin, "*")}}
	for _, o := range orgs {
		id := strconv.FormatInt(o.ID, 10)
		orgScopes = append(orgScopes, scopeItem{ID: id, Label: o.Name, Checked: has(directory.PermOrgAdmin, id)})
	}
	domScopes := func(perm string) []scopeItem {
		out := []scopeItem{{ID: "*", Label: "All domains", Checked: has(perm, "*")}}
		for _, d := range domains {
			id := strconv.FormatInt(d.ID, 10)
			out = append(out, scopeItem{ID: id, Label: d.Name, Checked: has(perm, id)})
		}
		return out
	}
	assigned := map[int64]bool{}
	for _, uid := range role.UserIDs {
		assigned[uid] = true
	}
	userChecks := make([]userCheck, 0, len(users))
	for _, u := range users {
		userChecks = append(userChecks, userCheck{ID: u.ID, Label: u.Username, Assigned: assigned[u.ID]})
	}
	data["Caps"], data["OrgAdmin"], data["Users"] = caps, orgScopes, userChecks
	data["DomainAdmin"] = domScopes(directory.PermDomainAdmin)
	data["DomainRO"] = domScopes(directory.PermDomainAdminRO)
}

// handleUIUpdateRole saves the role editor form: it rebuilds the permission set
// from the checkboxes and the user assignments from the multi-select, then
// replaces the role wholesale and re-renders the editor.
func (s *Server) handleUIUpdateRole(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	id, ok := roleIDParam(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return
	}
	var userIDs []int64
	for _, v := range r.Form["user"] {
		if uid, err := strconv.ParseInt(v, 10, 64); err == nil {
			userIDs = append(userIDs, uid)
		}
	}
	found, err := s.dir.UpdateRole(id, r.PostFormValue("name"), r.PostFormValue("description"), rolePermsFromForm(r), userIDs)
	if err != nil {
		role, _, roleErr := s.dir.GetRole(id)
		s.render(w, "role-editor", s.roleDetailData(r, role, roleErr, s.notice("Could not save role.", err), false))
		return
	}
	if !found {
		http.Error(w, "no such role", http.StatusNotFound)
		return
	}
	role, _, roleErr := s.dir.GetRole(id)
	s.render(w, "role-editor", s.roleDetailData(r, role, roleErr, "", true))
}

// rolePermsFromForm rebuilds a role's permission set from the editor checkboxes:
// the unscoped capability boxes plus the scoped org/domain selections (each value
// is a specific id or "*").
func rolePermsFromForm(r *http.Request) []directory.Permission {
	var perms []directory.Permission
	for _, name := range r.Form["cap"] {
		perms = append(perms, directory.Permission{Name: name})
	}
	for _, v := range r.Form["orgadmin"] {
		perms = append(perms, directory.Permission{Name: directory.PermOrgAdmin, Params: v})
	}
	for _, v := range r.Form["domainadmin"] {
		perms = append(perms, directory.Permission{Name: directory.PermDomainAdmin, Params: v})
	}
	for _, v := range r.Form["domainadminro"] {
		perms = append(perms, directory.Permission{Name: directory.PermDomainAdminRO, Params: v})
	}
	return perms
}

// handleUIDeleteRole deletes a role and redirects the browser to the roles list.
func (s *Server) handleUIDeleteRole(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	id, ok := roleIDParam(w, r)
	if !ok {
		return
	}
	if _, err := s.dir.DeleteRole(id); err != nil {
		s.fail(w, "could not delete role", err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/admin/ui/roles")
	w.WriteHeader(http.StatusOK)
}
