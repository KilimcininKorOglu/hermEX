package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/mapi"
)

// folderRightsLevels are the named permission presets ([MS-OXWSFOLD] permission
// levels), highest to lowest. Each maps to an exact rights bitmask in mapi; the admin
// UI offers them as a dropdown and the API reports the matching name for a member's
// stored bitmask. A bitmask matching none (e.g. a bare free/busy grant) is "Custom".
var folderRightsLevels = []struct {
	Name   string
	Rights uint32
}{
	{"Owner", mapi.RightsOwner},
	{"Publishing Editor", mapi.RightsPublishingEditor},
	{"Editor", mapi.RightsEditor},
	{"Publishing Author", mapi.RightsPublishingAuthor},
	{"Author", mapi.RightsAuthor},
	{"Nonediting Author", mapi.RightsNoneditingAuthor},
	{"Reviewer", mapi.RightsReviewer},
	{"Contributor", mapi.RightsContributor},
	{"None", mapi.RightsNone},
}

// rightsLevelName returns the named level matching an exact rights bitmask, or
// "Custom" for any other combination.
func rightsLevelName(rights uint32) string {
	for _, l := range folderRightsLevels {
		if l.Rights == rights {
			return l.Name
		}
	}
	return "Custom"
}

// folderJSON is one folder in the user's tree for the API.
type folderJSON struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"displayName"`
	ParentID    *int64 `json:"parentID,omitempty"`
}

// folderMemberJSON is one member of a folder's permission table for the API.
type folderMemberJSON struct {
	MemberID int64  `json:"memberID"`
	Name     string `json:"name"`
	Rights   uint32 `json:"rights"`
	Level    string `json:"level"`
}

// resolveMaildir looks up the user named in the request path and returns their
// mailbox path, writing a 404/500 and reporting ok=false when it cannot.
func (s *Server) resolveMaildir(w http.ResponseWriter, r *http.Request) (string, bool) {
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return "", false
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return "", false
	}
	return u.Maildir, true
}

// canonicalMember lowercases a typed member address and confirms it names a real
// user, returning the canonical form to store. An empty, alias, or unknown address is
// rejected (ok=false). This is the folder-permission counterpart of the resolution
// the protocol grant path performs: ResolvePermission compares the stored member name
// verbatim against the authenticated login, so a grant stored under a name no login
// resolves to (a typo, or an alias the primary login never matches) would be silently
// inert. Rejecting it here turns that silent miss into an explicit error.
func (s *Server) canonicalMember(username string) (member string, ok bool, err error) {
	member = strings.ToLower(strings.TrimSpace(username))
	if member == "" {
		return "", false, nil
	}
	_, found, err := s.dir.GetUser(member)
	if err != nil || !found {
		return "", false, err
	}
	return member, true, nil
}

// handleListUserFolders returns a user's folder tree (system administrators only).
func (s *Server) handleListUserFolders(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	folders, err := s.store.ListFolders(maildir)
	if err != nil {
		http.Error(w, "could not read folders", http.StatusInternalServerError)
		return
	}
	out := make([]folderJSON, 0, len(folders))
	for _, f := range folders {
		out = append(out, folderJSON{ID: f.ID, DisplayName: f.DisplayName, ParentID: f.ParentID})
	}
	writeJSON(w, out)
}

// handleListFolderPermissions returns the permission members of one of a user's
// folders (system administrators only).
func (s *Server) handleListFolderPermissions(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	fid, err := strconv.ParseInt(r.PathValue("fid"), 10, 64)
	if err != nil {
		http.Error(w, "invalid folder id", http.StatusBadRequest)
		return
	}
	perms, err := s.store.ListFolderPermissions(maildir, fid)
	if err != nil {
		http.Error(w, "could not read permissions", http.StatusInternalServerError)
		return
	}
	out := make([]folderMemberJSON, 0, len(perms))
	for _, p := range perms {
		out = append(out, folderMemberJSON{MemberID: p.MemberID, Name: p.Name, Rights: p.Rights, Level: rightsLevelName(p.Rights)})
	}
	writeJSON(w, out)
}

// handleSetFolderPermission grants or updates one member's rights on a folder
// (system administrators only). The member is addressed by username; an existing
// member's rights are replaced, a new member is added.
func (s *Server) handleSetFolderPermission(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	fid, err := strconv.ParseInt(r.PathValue("fid"), 10, 64)
	if err != nil {
		http.Error(w, "invalid folder id", http.StatusBadRequest)
		return
	}
	var in struct {
		Username string `json:"username"`
		Rights   uint32 `json:"rights"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	member, ok, err := s.canonicalMember(in.Username)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such member user", http.StatusNotFound)
		return
	}
	if err := s.store.SetFolderPermission(maildir, fid, member, in.Rights); err != nil {
		http.Error(w, "could not set permission: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRemoveFolderPermission drops one member from a folder's permission table
// (system administrators only), addressed by the wire member id in the query.
func (s *Server) handleRemoveFolderPermission(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	fid, err := strconv.ParseInt(r.PathValue("fid"), 10, 64)
	if err != nil {
		http.Error(w, "invalid folder id", http.StatusBadRequest)
		return
	}
	memberID, err := strconv.ParseInt(r.URL.Query().Get("memberID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid member id", http.StatusBadRequest)
		return
	}
	if err := s.store.RemoveFolderPermission(maildir, fid, memberID); err != nil {
		http.Error(w, "could not remove permission: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// renderFolderPerms lists a folder's permission members and renders the permission
// panel (the htmx swap target), carrying an optional error message. A zero fid means
// no folder is selected and the panel renders empty.
func (s *Server) renderFolderPerms(w http.ResponseWriter, email, maildir string, fid int64, csrf, errMsg string) {
	if fid == 0 {
		s.render(w, "folder-perms", map[string]any{"Email": email, "CSRF": csrf})
		return
	}
	perms, err := s.store.ListFolderPermissions(maildir, fid)
	if err != nil && errMsg == "" {
		errMsg = "Could not read permissions: " + err.Error()
	}
	members := make([]folderMemberJSON, 0, len(perms))
	for _, p := range perms {
		members = append(members, folderMemberJSON{MemberID: p.MemberID, Name: p.Name, Rights: p.Rights, Level: rightsLevelName(p.Rights)})
	}
	s.render(w, "folder-perms", map[string]any{
		"Email": email, "CSRF": csrf, "FID": fid, "Members": members, "Levels": folderRightsLevels, "Error": errMsg,
	})
}

// uiFolderUser resolves the user named in a UI folder request; on failure it renders
// the panel with an error and reports ok=false.
func (s *Server) uiFolderUser(w http.ResponseWriter, r *http.Request) (directory.UserDetail, bool) {
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil || !ok {
		s.render(w, "folder-perms", map[string]any{"Error": "No such user."})
		return directory.UserDetail{}, false
	}
	return u, true
}

// handleUIFolderPerms renders the permission panel for the folder selected in the
// detail form's folder picker.
func (s *Server) handleUIFolderPerms(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	u, ok := s.uiFolderUser(w, r)
	if !ok {
		return
	}
	fid, _ := strconv.ParseInt(r.FormValue("fid"), 10, 64)
	s.renderFolderPerms(w, u.Username, u.Maildir, fid, csrfCookieValue(r), "")
}

// handleUISetFolderPerm grants or updates a member's rights on the selected folder
// from the panel's add form and re-renders the panel.
func (s *Server) handleUISetFolderPerm(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	u, ok := s.uiFolderUser(w, r)
	if !ok {
		return
	}
	fid, _ := strconv.ParseInt(r.PostFormValue("fid"), 10, 64)
	rights, _ := strconv.ParseUint(r.PostFormValue("rights"), 10, 32)
	member, memberOK, mErr := s.canonicalMember(r.PostFormValue("username"))
	errMsg := ""
	switch {
	case mErr != nil:
		errMsg = "Could not look up user: " + mErr.Error()
	case !memberOK:
		errMsg = "No such user. Grant to the recipient's primary address."
	default:
		if err := s.store.SetFolderPermission(u.Maildir, fid, member, uint32(rights)); err != nil {
			errMsg = "Could not grant: " + err.Error()
		}
	}
	s.renderFolderPerms(w, u.Username, u.Maildir, fid, csrfCookieValue(r), errMsg)
}

// handleUIRemoveFolderPerm drops a member from the selected folder and re-renders the
// panel.
func (s *Server) handleUIRemoveFolderPerm(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	u, ok := s.uiFolderUser(w, r)
	if !ok {
		return
	}
	fid, _ := strconv.ParseInt(r.PostFormValue("fid"), 10, 64)
	memberID, _ := strconv.ParseInt(r.PostFormValue("memberID"), 10, 64)
	errMsg := ""
	if err := s.store.RemoveFolderPermission(u.Maildir, fid, memberID); err != nil {
		errMsg = "Could not remove: " + err.Error()
	}
	s.renderFolderPerms(w, u.Username, u.Maildir, fid, csrfCookieValue(r), errMsg)
}
