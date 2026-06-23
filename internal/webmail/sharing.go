package webmail

import (
	"net/http"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// folderShareLevels are the standard MAPI permission profiles (highest to
// lowest). Each maps to an exact rights bitmask, so a stored grant that matches
// none reads as "Custom".
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

// shareRow is one member's access to a folder, shown in the sharing table. The
// special default/anonymous rows (member id <= 0) are flagged so the page can
// label them as the folder-wide fallback rather than a named person.
type shareRow struct {
	MemberID int64
	Name     string
	Level    string
	Special  bool
}

// sharingView is the folder-sharing page model: a folder picker and, for the
// selected folder, who may access it at what level.
type sharingView struct {
	Folder  string
	Folders []folderView
	Rows    []shareRow
}

// handleFolderSharing renders the folder-sharing page for the user's OWN mailbox:
// pick a folder and see who has access at what level. This is the read side of
// folder sharing; it acts only on the caller's own mailbox, so any authenticated
// session suffices. Granting/revoking is not wired here yet: a self-service grant
// must store the grantee's canonical login (the name ResolvePermission matches a
// session against), and webmail's directory interface exposes no address-to-login
// resolver, only Resolve (which also accepts aliases).
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

	folder := r.URL.Query().Get("folder")
	view := sharingView{Folder: folder, Folders: s.folderViews(sess.mailboxPath)}
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
