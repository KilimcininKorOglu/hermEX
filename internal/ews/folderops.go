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
		writeSOAPFault(w, "ErrorInvalidRequest", "CreateFolder: "+err.Error())
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
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
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
		writeSOAPFault(w, "ErrorInvalidRequest", "DeleteFolder: "+err.Error())
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	var msgs []folderResponseMessage
	for _, ref := range req.FolderIDs.Folders {
		fid, err := oxews.DecodeFolderID(ref.ID)
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
// applies only folder:DisplayName (a rename); other fields are accepted but not
// applied, matching the reference's silent drop of unmapped fields.
type setFolderField struct {
	FieldURI struct {
		URI string `xml:"FieldURI,attr"`
	} `xml:"FieldURI"`
	Folder struct {
		DisplayName *string `xml:"DisplayName"`
	} `xml:"Folder"`
}

type updateFolderResponse struct {
	XMLName  xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateFolderResponse"`
	Messages []folderResponseMessage `xml:"ResponseMessages>UpdateFolderResponseMessage"`
}

// handleUpdateFolder answers UpdateFolder: it applies a folder:DisplayName
// SetFolderField as an in-place rename. A well-known (distinguished) folder's name
// is fixed — renaming it would desync the IMAP well-known projection — so it is
// refused. Other updatable fields are accepted as a no-op success, as the
// reference silently drops fields it has no converter for.
func (s *Server) handleUpdateFolder(w http.ResponseWriter, inner []byte, sess *session) {
	var req updateFolderRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "UpdateFolder: "+err.Error())
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	var msgs []folderResponseMessage
	for _, ch := range req.FolderChanges.Changes {
		targets := resolveTargets(ch.folderRefs)
		if len(targets) != 1 {
			msgs = append(msgs, folderError("ErrorInvalidRequest"))
			continue
		}
		if !targets[0].ok {
			msgs = append(msgs, folderError(targets[0].code))
			continue
		}
		fid := targets[0].fid

		var newName string
		for _, set := range ch.Updates.Sets {
			if set.FieldURI.URI == "folder:DisplayName" && set.Folder.DisplayName != nil {
				newName = *set.Folder.DisplayName
			}
		}
		if newName == "" {
			// No supported (or empty) rename — accept as a no-op, returning the id.
			msgs = append(msgs, folderOK(fid))
			continue
		}
		if fid < mapi.PrivateFIDUnassignedStart {
			msgs = append(msgs, folderError("ErrorAccessDenied"))
			continue
		}
		switch err := st.SetFolderName(fid, newName); {
		case err == nil:
			msgs = append(msgs, folderOK(fid))
		case errors.Is(err, objectstore.ErrFolderExists):
			msgs = append(msgs, folderError("ErrorFolderExists"))
		case errors.Is(err, objectstore.ErrNotFound):
			msgs = append(msgs, folderError("ErrorFolderNotFound"))
		default:
			msgs = append(msgs, folderError("ErrorFolderSave"))
		}
	}
	writeResponse(w, updateFolderResponse{Messages: msgs})
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
		writeSOAPFault(w, "ErrorInvalidRequest", "MoveCopyFolder: "+err.Error())
		return
	}
	dests := resolveTargets(req.ToFolderID)
	if len(dests) != 1 || !dests[0].ok {
		code := "ErrorInvalidRequest"
		if len(dests) == 1 && dests[0].code != "" {
			code = dests[0].code
		}
		writeResponse(w, moveCopyResponse(copy, []folderResponseMessage{folderError(code)}))
		return
	}
	dest := dests[0].fid
	var destArg *int64
	if dest != mapi.PrivateFIDIPMSubtree && dest != mapi.PrivateFIDRoot {
		d := dest
		destArg = &d
	}

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	var msgs []folderResponseMessage
	for _, src := range resolveTargets(req.FolderIDs) {
		if !src.ok {
			msgs = append(msgs, folderError(src.code))
			continue
		}
		fid := src.fid
		if !copy && fid < mapi.PrivateFIDUnassignedStart {
			msgs = append(msgs, folderError("ErrorMoveDistinguishedFolder"))
			continue
		}
		props, err := st.GetFolderProperties(fid, mapi.PrDisplayName)
		if err != nil {
			msgs = append(msgs, folderError("ErrorFolderNotFound"))
			continue
		}
		name, _ := props.Get(mapi.PrDisplayName)
		folderName, _ := name.(string)
		// Reject a destination name collision. A move excludes the folder itself
		// (moving it to where it already sits is a no-op, not a collision); a copy
		// does not (a copy beside an identically named sibling is a real clash).
		if existing, ok, err := st.FolderByName(destArg, folderName); err != nil {
			msgs = append(msgs, folderError("ErrorFolderNotFound"))
			continue
		} else if ok && (copy || existing != fid) {
			msgs = append(msgs, folderError("ErrorFolderExists"))
			continue
		}
		if copy {
			newID, err := st.CopyFolder(fid, dest, folderName, true)
			msgs = append(msgs, moveCopyResult(newID, err))
		} else {
			msgs = append(msgs, moveCopyResult(fid, st.RenameFolder(fid, &dest, folderName)))
		}
	}
	writeResponse(w, moveCopyResponse(copy, msgs))
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
