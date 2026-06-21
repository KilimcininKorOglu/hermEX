package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"hermex/internal/directory"
)

// permissionDef describes one assignable permission type for the role editor:
// its name and the kind of scope it takes ("" none, "org", or "domain").
type permissionDef struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

// permissionDefs is the fixed catalog of permission types a role may grant. The
// scope drives the editor: an unscoped type takes no parameter, an org/domain
// type takes a specific id or "*" for all.
var permissionDefs = []permissionDef{
	{directory.PermSystemAdmin, ""},
	{directory.PermSystemAdminRO, ""},
	{directory.PermOrgAdmin, "org"},
	{directory.PermDomainAdmin, "domain"},
	{directory.PermDomainAdminRO, "domain"},
	{directory.PermDomainPurge, ""},
	{directory.PermResetPasswd, ""},
}

// roleIDParam parses the {roleID} path value, writing a 400 when it is not a
// number. The role routes are system-admin only (their requireSystem wrapper
// authorizes), so this only validates the format.
func roleIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("roleID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid role id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// roleInput is the JSON body for creating or updating a named role.
type roleInput struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Permissions []directory.Permission `json:"permissions"`
	UserIDs     []int64                `json:"userIDs"`
}

// handleRolesList lists every named role with its user and permission counts.
func (s *Server) handleRolesList(w http.ResponseWriter, _ *http.Request) {
	roles, err := s.dir.ListRoles()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, roles)
}

// handleRolePermissions returns the catalog of assignable permission types.
func (s *Server) handleRolePermissions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, permissionDefs)
}

// handleRoleCreate creates a named role from the JSON body, returning 201 with
// its new id. A validation error (bad name or permission) is a 400.
func (s *Server) handleRoleCreate(w http.ResponseWriter, r *http.Request) {
	var in roleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	id, err := s.dir.CreateRole(in.Name, in.Description, in.Permissions, in.UserIDs)
	if err != nil {
		http.Error(w, "could not create role: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"id": id})
}

// handleRoleGet returns one role with its full permission set and assigned users,
// 404 for an unknown id.
func (s *Server) handleRoleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := roleIDParam(w, r)
	if !ok {
		return
	}
	role, found, err := s.dir.GetRole(id)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such role", http.StatusNotFound)
		return
	}
	writeJSON(w, role)
}

// handleRoleUpdate replaces a role's name, description, permission set, and user
// assignments. A validation error is a 400, an unknown id a 404.
func (s *Server) handleRoleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := roleIDParam(w, r)
	if !ok {
		return
	}
	var in roleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	found, err := s.dir.UpdateRole(id, in.Name, in.Description, in.Permissions, in.UserIDs)
	if err != nil {
		http.Error(w, "could not update role: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !found {
		http.Error(w, "no such role", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRoleDelete removes a role, 404 for an unknown id.
func (s *Server) handleRoleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := roleIDParam(w, r)
	if !ok {
		return
	}
	found, err := s.dir.DeleteRole(id)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such role", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
