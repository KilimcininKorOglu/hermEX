package webmail2api

import (
	"net/http"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// aclSanitizeRights masks a client-supplied MS-OXCPERM Frights bitfield down to
// the rights a client may set over the wire (RightsMaxROP), dropping any reserved
// or reference-private bit before storage, the same ingest allowlist
// ModifyPermissions enforces. The SPA sends the exact Frights union of the chosen
// permission level (or a custom per-right combination), so the stored bitmask
// round-trips back to the named level on read.
func aclSanitizeRights(rights uint32) uint32 {
	return rights & mapi.RightsMaxROP
}

// folderDescendants returns the ids of every folder nested under root (children,
// grandchildren, and so on), not including root itself, for a recursive grant.
func folderDescendants(folders []objectstore.FolderInfo, root int64) []int64 {
	children := map[int64][]int64{}
	for _, f := range folders {
		if f.ParentID != nil {
			children[*f.ParentID] = append(children[*f.ParentID], f.ID)
		}
	}
	var out []int64
	queue := append([]int64(nil), children[root]...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		out = append(out, id)
		queue = append(queue, children[id]...)
	}
	return out
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
		logError("acl-open-store", err, logging.Fields{"user": c.Email})
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
		logError("acl-list", err, logging.Fields{"user": c.Email, "folder": fid})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read permissions"})
		return
	}
	acl := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		if e.MemberID <= 0 { // skip the default/anonymous rows
			continue
		}
		acl = append(acl, map[string]any{"Grantee": e.Name, "Rights": e.Rights})
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
		Grantee   string `json:"grantee"`
		Rights    uint32 `json:"rights"` // MS-OXCPERM Frights bitfield
		Recursive bool   `json:"recursive"`
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
	change := []objectstore.PermissionChange{{Op: objectstore.PermAdd, Username: login, Rights: aclSanitizeRights(body.Rights)}}
	if err := st.ModifyPermissions(fid, false, change); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not grant access"})
		return
	}
	// When recursive, copy the same grant to every subfolder ([MS-OXCPERM] apply
	// permissions recursively); a subfolder that fails to take the grant is
	// skipped rather than failing the whole operation. The user is told the grant
	// succeeded either way, so a skipped subfolder is only visible here: without
	// this line, an operator has no way to learn that a share is partial.
	if body.Recursive {
		if folders, err := st.ListFolders(); err == nil {
			for _, sub := range folderDescendants(folders, fid) {
				if err := st.ModifyPermissions(sub, false, change); err != nil {
					logError("recursive-grant", err, logging.Fields{"grantee": logSafe(login), "folder": sub})
				}
			}
		}
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
