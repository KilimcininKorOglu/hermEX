package webmail2api

import (
	"net/http"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// aclLetters renders mapi folder rights as an RFC 4314-style letters string the
// SPA shows (l=lookup, r=read, s=seen, i=insert, w=write, d=delete, a=admin).
func aclLetters(r uint32) string {
	var b strings.Builder
	if r&mapi.FrightsVisible != 0 {
		b.WriteByte('l')
	}
	if r&mapi.FrightsReadAny != 0 {
		b.WriteString("rs")
	}
	if r&mapi.FrightsCreate != 0 {
		b.WriteByte('i')
	}
	if r&(mapi.FrightsEditAny|mapi.FrightsEditOwned) != 0 {
		b.WriteByte('w')
	}
	if r&(mapi.FrightsDeleteAny|mapi.FrightsDeleteOwned) != 0 {
		b.WriteByte('d')
	}
	if r&mapi.FrightsOwner != 0 {
		b.WriteByte('a')
	}
	return b.String()
}

// aclRightsToFrights maps the SPA's RFC 4314 permission-level numbers (reviewer=3,
// author=27, editor=239) to the matching mapi folder-rights preset.
func aclRightsToFrights(n int) uint32 {
	switch {
	case n >= 239:
		return mapi.RightsEditor
	case n >= 27:
		return mapi.RightsAuthor
	default:
		return mapi.RightsReviewer
	}
}

// aclStore opens the mailbox for a folder-ACL request. Self-service folder sharing
// is over the own mailbox only; sharing another owner's folders is not offered
// here, so a non-self owner is refused.
func (s *Server) aclStore(w http.ResponseWriter, r *http.Request, c sessionClaims) (*objectstore.Store, bool) {
	if owner := r.PathValue("owner"); owner != "" && !strings.EqualFold(owner, c.Email) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return nil, false
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return nil, false
	}
	return st, true
}

// aclFolderID resolves the {mailbox} path segment (a folder slug or display name)
// to a folder id.
func aclFolderID(st *objectstore.Store, name string) (int64, bool) {
	if fid, ok := folderFID(strings.ToLower(name)); ok {
		return fid, true
	}
	return folderByName(st, name)
}

func (s *Server) handleGetACL(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, ok := s.aclStore(w, r, c)
	if !ok {
		return
	}
	defer st.Close()
	fid, ok := aclFolderID(st, r.PathValue("mailbox"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown folder"})
		return
	}
	entries, err := st.ListPermissions(fid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read permissions"})
		return
	}
	acl := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		if e.MemberID <= 0 { // skip the default/anonymous rows
			continue
		}
		acl = append(acl, map[string]any{"Grantee": e.Name, "Rights": aclLetters(e.Rights)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"owner": c.Email, "mailbox": r.PathValue("mailbox"), "acl": acl})
}

func (s *Server) handleSetACL(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Grantee string `json:"grantee"`
		Rights  int    `json:"rights"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if strings.TrimSpace(body.Grantee) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a grantee address is required"})
		return
	}
	st, ok := s.aclStore(w, r, c)
	if !ok {
		return
	}
	defer st.Close()
	fid, ok := aclFolderID(st, r.PathValue("mailbox"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown folder"})
		return
	}
	// Resolve to the canonical mailbox login so the grant matches the store's
	// permission identity.
	login := strings.TrimSpace(body.Grantee)
	if res, ok := s.accounts.(directory.CanonicalResolver); ok {
		l, ok := res.CanonicalLogin(login)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no mailbox matches that address"})
			return
		}
		login = l
	}
	if strings.EqualFold(login, c.Email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "you already own this folder"})
		return
	}
	change := []objectstore.PermissionChange{{Op: objectstore.PermAdd, Username: login, Rights: aclRightsToFrights(body.Rights)}}
	if err := st.ModifyPermissions(fid, false, change); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not grant access"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Server) handleDeleteACL(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, ok := s.aclStore(w, r, c)
	if !ok {
		return
	}
	defer st.Close()
	fid, ok := aclFolderID(st, r.PathValue("mailbox"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown folder"})
		return
	}
	grantee := r.PathValue("grantee")
	entries, _ := st.ListPermissions(fid)
	var memberID int64
	for _, e := range entries {
		if e.MemberID > 0 && strings.EqualFold(e.Name, grantee) {
			memberID = e.MemberID
			break
		}
	}
	if memberID == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such grantee"})
		return
	}
	if err := st.ModifyPermissions(fid, false, []objectstore.PermissionChange{{Op: objectstore.PermRemove, MemberID: memberID}}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke access"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
