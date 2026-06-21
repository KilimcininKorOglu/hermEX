package admin

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/publicfolder"
)

// anyoneGrantee is the literal token an administrator types to grant a public
// folder to every authenticated user in the domain. It maps to the store's
// "default" member, which a per-domain public store scopes to that domain (a
// caller only ever reaches their own domain's store), so "anyone" means
// "anyone in this domain" without a domain column on the permission row.
const anyoneGrantee = "anyone"

// pubGrantView is one member row of a public folder's permission table for display:
// the default member is relabelled "anyone", and the rights bitmask is named.
type pubGrantView struct {
	MemberID int64
	Name     string
	Level    string
}

// pubFolderView is one public folder with its grants, projected for the admin page.
type pubFolderView struct {
	ID     int64
	Name   string
	Grants []pubGrantView
}

// publicFolderViews loads a domain's public folders and projects them for display,
// relabelling the default member as "anyone" and naming each rights level.
func (s *Server) publicFolderViews(domain string) ([]pubFolderView, error) {
	folders, err := s.pub.Folders(domain)
	if err != nil {
		return nil, err
	}
	out := make([]pubFolderView, 0, len(folders))
	for _, f := range folders {
		grants := make([]pubGrantView, 0, len(f.Grants))
		for _, g := range f.Grants {
			name := g.Name
			if g.MemberID == mapi.MemberIDDefault {
				name = anyoneGrantee
			}
			grants = append(grants, pubGrantView{MemberID: g.MemberID, Name: name, Level: rightsLevelName(g.Rights)})
		}
		out = append(out, pubFolderView{ID: f.ID, Name: f.DisplayName, Grants: grants})
	}
	return out, nil
}

// renderPublicPanel renders the folder/grant panel for one domain (the htmx swap
// target), carrying an optional error. An empty domain renders the panel empty.
func (s *Server) renderPublicPanel(w http.ResponseWriter, domain, csrf, errMsg string) {
	data := map[string]any{"Domain": domain, "CSRF": csrf, "Levels": folderRightsLevels}
	if domain != "" {
		folders, err := s.publicFolderViews(domain)
		if err != nil && errMsg == "" {
			errMsg = "Could not read public folders: " + err.Error()
		}
		data["Folders"] = folders
	}
	data["Error"] = errMsg
	s.render(w, "public-folders-panel", data)
}

// handleUIPublicFolders renders the public-folders management page: a domain picker
// and, when a domain is selected, that domain's folders and grants (system admins).
func (s *Server) handleUIPublicFolders(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	domains, err := s.dir.ListDomains()
	if err != nil {
		http.Error(w, "could not list domains", http.StatusInternalServerError)
		return
	}
	domain := r.FormValue("domain")
	data := map[string]any{
		"Nav": "publicfolders", "CSRF": csrfCookieValue(r),
		"Domains": domains, "Domain": domain, "Levels": folderRightsLevels,
	}
	if domain != "" {
		folders, ferr := s.publicFolderViews(domain)
		if ferr != nil {
			data["Error"] = "Could not read public folders: " + ferr.Error()
		}
		data["Folders"] = folders
	}
	s.render(w, "public_folders.html", data)
}

// handleUIPublicFoldersPanel renders just the folder/grant panel for the domain the
// picker selected (the htmx swap when the domain changes).
func (s *Server) handleUIPublicFoldersPanel(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.renderPublicPanel(w, r.FormValue("domain"), csrfCookieValue(r), "")
}

// handleUICreatePublicFolder provisions the domain's public store if absent and
// creates a folder from the panel form, then re-renders the panel.
func (s *Server) handleUICreatePublicFolder(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	domain := r.PostFormValue("domain")
	name := strings.TrimSpace(r.PostFormValue("name"))
	errMsg := ""
	switch {
	case domain == "":
		errMsg = "Select a domain first."
	case name == "":
		errMsg = "Enter a folder name."
	default:
		if _, err := s.pub.CreateFolder(domain, name); err != nil {
			errMsg = "Could not create folder: " + err.Error()
		}
	}
	s.renderPublicPanel(w, domain, csrfCookieValue(r), errMsg)
}

// handleUIDeletePublicFolder deletes a public folder and re-renders the panel.
func (s *Server) handleUIDeletePublicFolder(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	domain := r.PostFormValue("domain")
	fid, _ := strconv.ParseInt(r.PostFormValue("fid"), 10, 64)
	errMsg := ""
	if err := s.pub.DeleteFolder(domain, fid); err != nil {
		errMsg = "Could not delete folder: " + err.Error()
	}
	s.renderPublicPanel(w, domain, csrfCookieValue(r), errMsg)
}

// handleUISetPublicGrant adds or updates a member's rights on a public folder from
// the panel's grant form, then re-renders the panel. The grantee is either the
// literal "anyone" (the org-wide default member) or a real user in the same domain.
func (s *Server) handleUISetPublicGrant(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	domain := r.PostFormValue("domain")
	fid, _ := strconv.ParseInt(r.PostFormValue("fid"), 10, 64)
	rights, _ := strconv.ParseUint(r.PostFormValue("rights"), 10, 32)
	change, errMsg := s.publicGrantChange(domain, r.PostFormValue("grantee"), uint32(rights))
	if errMsg == "" {
		if err := s.pub.Grant(domain, fid, change); err != nil {
			errMsg = "Could not grant: " + err.Error()
		}
	}
	s.renderPublicPanel(w, domain, csrfCookieValue(r), errMsg)
}

// handleUIRemovePublicGrant drops a member from a public folder and re-renders the
// panel.
func (s *Server) handleUIRemovePublicGrant(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	domain := r.PostFormValue("domain")
	fid, _ := strconv.ParseInt(r.PostFormValue("fid"), 10, 64)
	memberID, _ := strconv.ParseInt(r.PostFormValue("memberID"), 10, 64)
	errMsg := ""
	change := objectstore.PermissionChange{Op: objectstore.PermRemove, MemberID: memberID}
	if err := s.pub.Grant(domain, fid, change); err != nil {
		errMsg = "Could not remove grant: " + err.Error()
	}
	s.renderPublicPanel(w, domain, csrfCookieValue(r), errMsg)
}

// publicGrantChange validates a grantee and builds the permission change to add. The
// grantee is "anyone" (the default member) or a real user whose primary address is
// in the same domain — a cross-domain grantee would be inert, since a caller only
// reaches their own domain's store, so it is rejected as a clear error rather than
// stored silently dead. It returns a non-empty errMsg on rejection.
func (s *Server) publicGrantChange(domain, grantee string, rights uint32) (objectstore.PermissionChange, string) {
	grantee = strings.ToLower(strings.TrimSpace(grantee))
	if grantee == "" {
		return objectstore.PermissionChange{}, "Enter a grantee (an address or \"anyone\")."
	}
	if grantee == anyoneGrantee {
		return objectstore.PermissionChange{Op: objectstore.PermAdd, MemberID: mapi.MemberIDDefault, Rights: rights}, ""
	}
	member, ok, err := s.canonicalMember(grantee)
	_, memberDomain, _ := strings.Cut(member, "@")
	switch {
	case err != nil:
		return objectstore.PermissionChange{}, "Could not look up user: " + err.Error()
	case !ok:
		return objectstore.PermissionChange{}, "No such user. Grant to \"anyone\" or a user's primary address."
	case !strings.EqualFold(memberDomain, domain):
		return objectstore.PermissionChange{}, "Grant a user in this domain, or \"anyone\"."
	}
	return objectstore.PermissionChange{Op: objectstore.PermAdd, Username: member, Rights: rights}, ""
}

// handleGetPublicFolders returns a domain's public folders with their grants as JSON
// (system administrators only). Read-only: it never provisions a store.
func (s *Server) handleGetPublicFolders(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		http.Error(w, "domain required", http.StatusBadRequest)
		return
	}
	folders, err := s.pub.Folders(domain)
	if err != nil {
		http.Error(w, "could not read public folders", http.StatusInternalServerError)
		return
	}
	writeJSON(w, publicFoldersJSON(folders))
}

// publicFolderJSON is one public folder with its grants for the JSON API.
type publicFolderJSON struct {
	ID     int64              `json:"id"`
	Name   string             `json:"name"`
	Grants []folderMemberJSON `json:"grants"`
}

// publicFoldersJSON projects the service folders to the JSON shape, naming each
// member's rights level and reporting the default member's wire id as stored.
func publicFoldersJSON(folders []publicfolder.FolderWithGrants) []publicFolderJSON {
	out := make([]publicFolderJSON, 0, len(folders))
	for _, f := range folders {
		grants := make([]folderMemberJSON, 0, len(f.Grants))
		for _, g := range f.Grants {
			grants = append(grants, folderMemberJSON{MemberID: g.MemberID, Name: g.Name, Rights: g.Rights, Level: rightsLevelName(g.Rights)})
		}
		out = append(out, publicFolderJSON{ID: f.ID, Name: f.DisplayName, Grants: grants})
	}
	return out
}
