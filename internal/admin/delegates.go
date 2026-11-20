package admin

import (
	"net/http"
	"slices"
	"strings"

	"hermex/internal/objectstore"
)

// handleUIUserDelegates saves the user's public-delegate list from the detail form
// (one address per line, whitespace-separated like the aliases form) and returns
// the refreshed status panel. This is the same per-mailbox list NSPI serves and a
// user edits from Outlook, so the admin console and the client manage one source
// of truth.
func (s *Server) handleUIUserDelegates(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = s.notice(userUnread, err)
	case !ok:
		data["Error"] = "userDetail.noSuchUser"
	default:
		list := strings.Fields(r.PostFormValue("delegates"))
		if foreign := s.outsideOwnerOrg(u.Username, list); foreign != "" {
			data["Error"] = msg("userDetail.granteeOutsideOrg", foreign)
			break
		}
		if err := s.store.SetDelegates(u.Maildir, list); err != nil {
			data["Error"] = s.notice("userDetail.saveDelegatesFailed", err)
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, r, "user-status", data)
}

// handleUIUserDelegateAccess sets the folder access one delegate holds on the
// mailbox's delegate folders: none, read (Reviewer) or write (Editor), the same
// levels webmail2 offers. Only a name already on the delegate list is accepted,
// so the form cannot hand folder rights to someone the list does not name.
func (s *Server) handleUIUserDelegateAccess(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	data := map[string]any{}
	if errKey := s.saveDelegateAccess(r); errKey != "" {
		data["Error"] = errKey
	} else {
		data["Saved"] = true
	}
	s.render(w, r, "user-status", data)
}

// saveDelegateAccess validates and stores one delegate's access level, returning
// the notice key for a refusal or a failure, or "" on success.
func (s *Server) saveDelegateAccess(r *http.Request) string {
	access := objectstore.DelegateAccess(r.PostFormValue("access"))
	if access != objectstore.DelegateNoAccess && access != objectstore.DelegateRead && access != objectstore.DelegateWrite {
		return "userDetail.delegateAccessInvalid"
	}
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	switch {
	case err != nil:
		return s.notice(userUnread, err)
	case !ok:
		return "userDetail.noSuchUser"
	}
	list, err := s.store.GetDelegates(u.Maildir)
	if err != nil {
		return s.notice("userDetail.saveDelegateAccessFailed", err)
	}
	grantee := strings.TrimSpace(r.PostFormValue("grantee"))
	if !slices.ContainsFunc(list, func(d string) bool { return strings.EqualFold(d, grantee) }) {
		return "userDetail.delegateAccessNotListed"
	}
	if err := s.store.SetDelegateAccess(u.Maildir, grantee, access); err != nil {
		return s.notice("userDetail.saveDelegateAccessFailed", err)
	}
	return ""
}
