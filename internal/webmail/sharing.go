package webmail

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// folderShareLevels are the standard MAPI permission profiles (highest to
// lowest), the same set the admin panel grants. Each maps to an exact rights
// bitmask, so a stored grant that matches none reads as "Custom".
var folderShareLevels = []struct {
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

// shareLevelName returns the named level matching an exact rights bitmask, or
// "Custom" for any other combination.
func shareLevelName(rights uint32) string {
	for _, l := range folderShareLevels {
		if l.Rights == rights {
			return l.Name
		}
	}
	return "Custom"
}

// shareLevelRights resolves a level name from the grant form to its bitmask.
func shareLevelRights(name string) (uint32, bool) {
	for _, l := range folderShareLevels {
		if l.Name == name {
			return l.Rights, true
		}
	}
	return 0, false
}

// shareRow is one member's access to a folder, shown in the sharing table. The
// special default/anonymous rows (member id <= 0) are flagged so the page labels
// them as the folder-wide fallback rather than a named person, and offers no
// revoke (the grant form manages named members only).
type shareRow struct {
	MemberID int64
	Name     string
	Level    string
	Special  bool
}

// sharingView is the folder-sharing page model: a folder picker and, for the
// selected folder, who may access it at what level plus the grant form.
type sharingView struct {
	Folder  string
	Folders []folderView
	Rows    []shareRow
	Levels  []string
	Error   string
}

// handleFolderSharing renders the folder-sharing page for the user's OWN mailbox:
// pick a folder and see who has access at what level. Sharing acts only on the
// caller's own mailbox, so any authenticated session suffices.
func (s *Server) handleFolderSharing(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()
	s.renderSharing(w, st, sess.mailboxPath, r.URL.Query().Get("folder"), "")
}

// handleFolderSharingSubmit grants or revokes one member's access to a folder of
// the caller's own mailbox, then re-renders the page. It carries no CSRF token, the
// established webmail convention (state changes rely on the SameSite session
// cookie, as the rules/safe-senders/recipient-access forms do).
func (s *Server) handleFolderSharingSubmit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()
	folders, err := st.ListFolders()
	if err != nil {
		http.Error(w, "cannot read folders", http.StatusInternalServerError)
		return
	}
	folder := r.FormValue("folder")
	folderID, found := resolveFolder(folders, folder)
	if !found {
		http.NotFound(w, r)
		return
	}

	errMsg := ""
	switch r.FormValue("op") {
	case "grant":
		errMsg = s.grantShare(st, folderID, sess.user, r.FormValue("member"), r.FormValue("level"))
	case "revoke":
		// Revoke a named member only; the special default/anonymous rows (id <= 0)
		// are not offered for removal here.
		if id, err := strconv.ParseInt(r.FormValue("memberid"), 10, 64); err == nil && id > 0 {
			if err := st.ModifyPermissions(folderID, false, []objectstore.PermissionChange{
				{Op: objectstore.PermRemove, MemberID: id},
			}); err != nil {
				errMsg = "Could not revoke access."
			}
		}
	}
	s.renderSharing(w, st, sess.mailboxPath, folder, errMsg)
}

// grantShare adds (or replaces) one member's rights on a folder, returning a
// user-facing message on failure and "" on success. The grantee address is
// resolved to its canonical login before storing, because ResolvePermission
// compares the stored member name verbatim against a session login; an address
// that resolves to no login is rejected rather than stored as an inert grant.
func (s *Server) grantShare(st *objectstore.Store, folderID int64, owner, member, level string) string {
	rights, ok := shareLevelRights(level)
	if !ok {
		return "Choose a permission level."
	}
	if strings.TrimSpace(member) == "" {
		return "Enter the email address to share with."
	}
	resolver, ok := s.accounts.(directory.CanonicalResolver)
	if !ok {
		return "Sharing is unavailable on this server."
	}
	login, ok := resolver.CanonicalLogin(member)
	if !ok {
		return "No mailbox matches that address."
	}
	if strings.EqualFold(login, strings.TrimSpace(owner)) {
		return "You already own this folder."
	}
	if err := st.ModifyPermissions(folderID, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: login, Rights: rights},
	}); err != nil {
		return "Could not grant access."
	}
	return ""
}

// renderSharing draws the sharing page for one folder (or just the picker when no
// folder is selected). st is the already-open own mailbox.
func (s *Server) renderSharing(w http.ResponseWriter, st *objectstore.Store, mailboxPath, folder, errMsg string) {
	view := sharingView{Folder: folder, Folders: s.folderViews(mailboxPath), Error: errMsg}
	for _, l := range folderShareLevels {
		view.Levels = append(view.Levels, l.Name)
	}
	if folder != "" {
		folders, err := st.ListFolders()
		if err != nil {
			http.Error(w, "cannot read folders", http.StatusInternalServerError)
			return
		}
		if folderID, found := resolveFolder(folders, folder); found {
			entries, err := st.ListPermissions(folderID)
			if err != nil {
				http.Error(w, "cannot read permissions", http.StatusInternalServerError)
				return
			}
			for _, e := range entries {
				view.Rows = append(view.Rows, shareRow{
					MemberID: e.MemberID,
					Name:     e.Name,
					Level:    shareLevelName(e.Rights),
					Special:  e.MemberID <= 0,
				})
			}
		}
	}
	s.render(w, "sharing", view)
}
