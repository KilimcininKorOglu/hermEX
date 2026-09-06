package ews

import (
	"encoding/xml"
	"errors"
	"net/http"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// --- CreateFolder ---

type createFolderRequest struct {
	ParentFolderID folderRefs `xml:"ParentFolderId"`
	Folders        struct {
		Folders []struct {
			DisplayName string `xml:"DisplayName"`
		} `xml:"Folder"`
	} `xml:"Folders"`
}

type createFolderResponse struct {
	XMLName  xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateFolderResponse"`
	Messages []folderResponseMessage `xml:"ResponseMessages>CreateFolderResponseMessage"`
}

// handleCreateFolder answers CreateFolder: it creates each requested folder under
// the parent (the IPM subtree root maps to a top-level folder).
func (s *Server) handleCreateFolder(w http.ResponseWriter, inner []byte, sess *session) {
	var req createFolderRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "CreateFolder: invalid request", err)
		return
	}
	targets := resolveTargets(req.ParentFolderID)
	if len(targets) == 0 {
		writeResponse(w, createFolderResponse{Messages: []folderResponseMessage{folderError("ErrorInvalidRequest")}})
		return
	}
	if !targets[0].ok {
		writeResponse(w, createFolderResponse{Messages: []folderResponseMessage{folderError(targets[0].code)}})
		return
	}
	parentFID := targets[0].fid
	var parent *int64
	if parentFID != mapi.PrivateFIDIPMSubtree && parentFID != mapi.PrivateFIDRoot {
		p := parentFID
		parent = &p
	}

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		s.soapFault(w, "ErrorInternalServerError", "an internal error occurred", err)
		return
	}
	defer st.Close()

	var msgs []folderResponseMessage
	for _, f := range req.Folders.Folders {
		if f.DisplayName == "" {
			msgs = append(msgs, folderError("ErrorInvalidRequest"))
			continue
		}
		fid, err := st.CreateFolder(parent, f.DisplayName)
		if err != nil {
			msgs = append(msgs, folderError("ErrorInternalServerError"))
			continue
		}
		elem := oxews.BuildFolder(oxews.FolderInput{FolderID: fid, DisplayName: f.DisplayName})
		msgs = append(msgs, folderResponseMessage{
			ResponseClass: "Success", ResponseCode: "NoError",
			Folders: &foldersWrap{Folders: []oxews.Folder{elem}},
		})
	}
	writeResponse(w, createFolderResponse{Messages: msgs})
}

// --- DeleteFolder ---

type deleteFolderRequest struct {
	DeleteType string `xml:"DeleteType,attr"`
	FolderIDs  struct {
		Folders []refID `xml:"FolderId"`
	} `xml:"FolderIds"`
}

type deleteFolderResponse struct {
	XMLName  xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolderResponse"`
	Messages []folderResponseMessage `xml:"ResponseMessages>DeleteFolderResponseMessage"`
}

// handleDeleteFolder answers DeleteFolder: it deletes user folders (cascading);
// built-in folders are protected (only ids at or above the unassigned range are
// deletable, matching the webmail folder-management guard).
func (s *Server) handleDeleteFolder(w http.ResponseWriter, inner []byte, sess *session) {
	var req deleteFolderRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "DeleteFolder: invalid request", err)
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		s.soapFault(w, "ErrorInternalServerError", "an internal error occurred", err)
		return
	}
	defer st.Close()

	var msgs []folderResponseMessage
	for _, ref := range req.FolderIDs.Folders {
		fid, _, err := oxews.DecodeFolderID(ref.ID)
		if err != nil {
			msgs = append(msgs, folderError("ErrorInvalidRequest"))
			continue
		}
		if fid < mapi.PrivateFIDUnassignedStart {
			msgs = append(msgs, folderError("ErrorDeleteDistinguishedFolder"))
			continue
		}
		if err := st.DeleteFolder(fid); err != nil {
			msgs = append(msgs, folderError("ErrorItemNotFound"))
			continue
		}
		msgs = append(msgs, folderResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"})
	}
	writeResponse(w, deleteFolderResponse{Messages: msgs})
}

// --- UpdateFolder ---

type updateFolderRequest struct {
	FolderChanges struct {
		Changes []folderChange `xml:"FolderChange"`
	} `xml:"FolderChanges"`
}

// folderChange targets one folder (by FolderId or DistinguishedFolderId, via the
// embedded folderRefs) with a set of field updates.
type folderChange struct {
	folderRefs
	Updates struct {
		Sets []setFolderField `xml:"SetFolderField"`
	} `xml:"Updates"`
}

// setFolderField carries a FieldURI and the new value inside a <Folder>. v1
// applies folder:DisplayName (a rename) and folder:PermissionSet (an access-control
// replace); other fields are accepted but not applied, matching the reference's
// silent drop of unmapped fields.
type setFolderField struct {
	FieldURI struct {
		URI string `xml:"FieldURI,attr"`
	} `xml:"FieldURI"`
	Folder struct {
		DisplayName   *string              `xml:"DisplayName"`
		PermissionSet *oxews.PermissionSet `xml:"PermissionSet"`
	} `xml:"Folder"`
}

type updateFolderResponse struct {
	XMLName  xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateFolderResponse"`
	Messages []folderResponseMessage `xml:"ResponseMessages>UpdateFolderResponseMessage"`
}

// handleUpdateFolder answers UpdateFolder: it applies a folder:DisplayName
// SetFolderField as an in-place rename and a folder:PermissionSet SetFolderField as
// a full access-control replace. A well-known (distinguished) folder's name is
// fixed, renaming it would desync the IMAP well-known projection, so a rename of
// one is refused; a permission change on one is allowed (sharing a well-known
// folder is legitimate and does not touch the name projection). Other updatable
// fields are accepted as a no-op success, as the reference silently drops fields it
// has no converter for.
func (s *Server) handleUpdateFolder(w http.ResponseWriter, inner []byte, sess *session) {
	var req updateFolderRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "UpdateFolder: invalid request", err)
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		s.soapFault(w, "ErrorInternalServerError", "an internal error occurred", err)
		return
	}
	defer st.Close()

	var msgs []folderResponseMessage
	for _, ch := range req.FolderChanges.Changes {
		msgs = append(msgs, s.updateOneFolder(st, ch))
	}
	writeResponse(w, updateFolderResponse{Messages: msgs})
}

// updateOneFolder applies one folder change: a rename, a permission set, or both.
func (s *Server) updateOneFolder(st *objectstore.Store, ch folderChange) folderResponseMessage {
	targets := resolveTargets(ch.folderRefs)
	if len(targets) != 1 {
		return folderError("ErrorInvalidRequest")
	}
	if !targets[0].ok {
		return folderError(targets[0].code)
	}
	fid := targets[0].fid
	newName, permSet := folderUpdates(ch)

	if newName != "" {
		if fid < mapi.PrivateFIDUnassignedStart {
			return folderError("ErrorAccessDenied")
		}
		if msg, ok := applyFolderRename(st, fid, newName); !ok {
			return msg
		}
	}
	if permSet != nil {
		if msg, ok := s.applyPermissionSet(st, fid, permSet); !ok {
			return msg
		}
	}
	return folderOK(fid)
}

// folderUpdates reads the two updates UpdateFolder honors out of a change's
// SetFolderField list.
func folderUpdates(ch folderChange) (newName string, permSet *oxews.PermissionSet) {
	for _, set := range ch.Updates.Sets {
		switch set.FieldURI.URI {
		case "folder:DisplayName":
			if set.Folder.DisplayName != nil {
				newName = *set.Folder.DisplayName
			}
		case "folder:PermissionSet":
			if set.Folder.PermissionSet != nil {
				permSet = set.Folder.PermissionSet
			}
		}
	}
	return newName, permSet
}

// applyFolderRename renames the folder, mapping store errors to response codes. It
// returns ok=true on success (with an empty message the caller ignores) and
// ok=false with the error message to emit.
func applyFolderRename(st *objectstore.Store, fid int64, newName string) (folderResponseMessage, bool) {
	switch err := st.SetFolderName(fid, newName); {
	case err == nil:
		return folderResponseMessage{}, true
	case errors.Is(err, objectstore.ErrFolderExists):
		return folderError("ErrorFolderExists"), false
	case errors.Is(err, objectstore.ErrNotFound):
		return folderError("ErrorFolderNotFound"), false
	default:
		return folderError("ErrorFolderSave"), false
	}
}

// applyPermissionSet replaces a folder's whole permission table with the wire
// PermissionSet, MS-OXWSFOLD UpdateFolder is a full ACL replace, not a diff. Each
// member's rights are masked to the client-sendable set and normalized as the store
// contract requires. A real member whose address does not resolve in the directory
// is skipped (matching the ROP permission path); because this is a full replace,
// skipping silently drops that member from the new ACL.
func (s *Server) applyPermissionSet(st *objectstore.Store, fid int64, set *oxews.PermissionSet) (folderResponseMessage, bool) {
	changes := make([]objectstore.PermissionChange, 0, len(set.Permissions))
	for _, p := range set.Permissions {
		memberID, username, ok := s.resolvePermissionUser(p.UserID)
		if !ok {
			continue
		}
		rights := mapi.NormalizeRights(oxews.PermissionRights(p)&mapi.RightsMaxROP, true)
		changes = append(changes, objectstore.PermissionChange{
			Op: objectstore.PermAdd, MemberID: memberID, Username: username, Rights: rights,
		})
	}
	if err := st.ModifyPermissions(fid, true, changes); err != nil {
		return folderError("ErrorFolderSave"), false
	}
	return folderResponseMessage{}, true
}

// resolvePermissionUser maps a wire UserId to a store permission member: a
// DistinguishedUser is the always-present Default (id 0) or Anonymous (id -1)
// member; a real member is keyed by its PrimarySmtpAddress, confirmed to exist in
// the directory. An unresolvable or identity-less entry yields ok=false so the
// caller skips it rather than faulting.
func (s *Server) resolvePermissionUser(u oxews.UserID) (memberID int64, username string, ok bool) {
	switch u.DistinguishedUser {
	case "Default":
		return mapi.MemberIDDefault, "", true
	case "Anonymous":
		return mapi.MemberIDAnonymous, "", true
	}
	smtp := u.PrimarySmtpAddress
	if smtp == "" {
		return 0, "", false
	}
	if s.accounts != nil {
		if _, ok := s.accounts.Resolve(smtp); !ok {
			return 0, "", false
		}
	}
	return 0, smtp, true
}

// --- MoveFolder / CopyFolder ---

// moveCopyFolderRequest is the shared shape of MoveFolder and CopyFolder: a single
// destination parent plus the folders to move or copy into it.
type moveCopyFolderRequest struct {
	ToFolderID folderRefs `xml:"ToFolderId"`
	FolderIDs  folderRefs `xml:"FolderIds"`
}

type moveFolderResponse struct {
	XMLName  xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MoveFolderResponse"`
	Messages []folderResponseMessage `xml:"ResponseMessages>MoveFolderResponseMessage"`
}

type copyFolderResponse struct {
	XMLName  xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CopyFolderResponse"`
	Messages []folderResponseMessage `xml:"ResponseMessages>CopyFolderResponseMessage"`
}

// handleMoveFolder reparents each folder under the destination, keeping its id.
func (s *Server) handleMoveFolder(w http.ResponseWriter, inner []byte, sess *session) {
	s.moveCopyFolders(w, inner, sess, false)
}

// handleCopyFolder copies each folder (recursively, with its contents) under the
// destination, returning the copy's new id.
func (s *Server) handleCopyFolder(w http.ResponseWriter, inner []byte, sess *session) {
	s.moveCopyFolders(w, inner, sess, true)
}

// moveCopyFolders is the shared MoveFolder/CopyFolder body. A move keeps the
// folder name and id and refuses a distinguished source (reparenting a well-known
// folder corrupts the hierarchy); a copy is recursive and assigns a new id, and a
// distinguished source is allowed (copying the Inbox into a user folder is
// legitimate). Both refuse a name already present in the destination
// (ErrorFolderExists) and report a cycle (a folder into its own subtree) as
// ErrorMoveCopyFailed.
func (s *Server) moveCopyFolders(w http.ResponseWriter, inner []byte, sess *session, copy bool) {
	var req moveCopyFolderRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "MoveCopyFolder: invalid request", err)
		return
	}
	dest, code := moveCopyFolderDest(req.ToFolderID)
	if code != "" {
		writeResponse(w, moveCopyResponse(copy, []folderResponseMessage{folderError(code)}))
		return
	}

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		s.soapFault(w, "ErrorInternalServerError", "an internal error occurred", err)
		return
	}
	defer st.Close()

	var msgs []folderResponseMessage
	for _, src := range resolveTargets(req.FolderIDs) {
		msgs = append(msgs, moveCopyOneFolder(st, src, dest, copy))
	}
	writeResponse(w, moveCopyResponse(copy, msgs))
}

// moveCopyFolderDest resolves the single destination folder a MoveFolder or
// CopyFolder names, reporting the response code refusing a malformed request.
func moveCopyFolderDest(refs folderRefs) (int64, string) {
	dests := resolveTargets(refs)
	if len(dests) != 1 || !dests[0].ok {
		if len(dests) == 1 && dests[0].code != "" {
			return 0, dests[0].code
		}
		return 0, "ErrorInvalidRequest"
	}
	return dests[0].fid, ""
}

// moveCopyOneFolder moves or copies one folder into the destination.
func moveCopyOneFolder(st *objectstore.Store, src folderTarget, dest int64, copy bool) folderResponseMessage {
	if !src.ok {
		return folderError(src.code)
	}
	fid := src.fid
	if !copy && fid < mapi.PrivateFIDUnassignedStart {
		return folderError("ErrorMoveDistinguishedFolder")
	}
	props, err := st.GetFolderProperties(fid, mapi.PrDisplayName)
	if err != nil {
		return folderError("ErrorFolderNotFound")
	}
	name, _ := props.Get(mapi.PrDisplayName)
	folderName, _ := name.(string)
	if code := checkFolderNameFree(st, dest, folderName, fid, copy); code != "" {
		return folderError(code)
	}
	if copy {
		newID, err := st.CopyFolder(fid, dest, folderName, true)
		return moveCopyResult(newID, err)
	}
	return moveCopyResult(fid, st.RenameFolder(fid, &dest, folderName))
}

// checkFolderNameFree rejects a destination name collision. A move excludes the
// folder itself (moving it to where it already sits is a no-op, not a collision);
// a copy does not (a copy beside an identically named sibling is a real clash).
func checkFolderNameFree(st *objectstore.Store, dest int64, folderName string, fid int64, copy bool) string {
	var parent *int64
	if dest != mapi.PrivateFIDIPMSubtree && dest != mapi.PrivateFIDRoot {
		parent = &dest
	}
	existing, ok, err := st.FolderByName(parent, folderName)
	if err != nil {
		return "ErrorFolderNotFound"
	}
	if ok && (copy || existing != fid) {
		return "ErrorFolderExists"
	}
	return ""
}

// moveCopyResult maps a move/copy store outcome to a response message carrying the
// resulting folder id on success.
func moveCopyResult(fid int64, err error) folderResponseMessage {
	switch {
	case err == nil:
		return folderOK(fid)
	case errors.Is(err, objectstore.ErrFolderCycle):
		return folderError("ErrorMoveCopyFailed")
	case errors.Is(err, objectstore.ErrFolderExists):
		return folderError("ErrorFolderExists")
	case errors.Is(err, objectstore.ErrNotFound):
		return folderError("ErrorFolderNotFound")
	default:
		return folderError("ErrorMoveCopyFailed")
	}
}

// moveCopyResponse wraps the response messages in the MoveFolder or CopyFolder
// response envelope.
func moveCopyResponse(copy bool, msgs []folderResponseMessage) any {
	if copy {
		return copyFolderResponse{Messages: msgs}
	}
	return moveFolderResponse{Messages: msgs}
}

// folderError builds an error folder response message.
func folderError(code string) folderResponseMessage {
	return folderResponseMessage{ResponseClass: "Error", ResponseCode: code}
}

// folderOK builds a success folder response message carrying the folder's id.
func folderOK(fid int64) folderResponseMessage {
	return folderResponseMessage{
		ResponseClass: "Success", ResponseCode: "NoError",
		Folders: &foldersWrap{Folders: []oxews.Folder{oxews.BuildFolder(oxews.FolderInput{FolderID: fid})}},
	}
}
