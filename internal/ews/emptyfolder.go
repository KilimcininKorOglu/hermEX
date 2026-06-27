package ews

import (
	"encoding/xml"
	"net/http"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// Folder emptying (MS-OXWSFOLD EmptyFolder) removes a folder's contents, keeping
// the folder itself. The DeleteType attribute selects the disposition of the
// items and DeleteSubFolders optionally removes the folder's subtree too. Unlike
// DeleteFolder, EmptyFolder operates on distinguished folders (Empty Deleted
// Items is its canonical use), so a well-known folder is emptied, not refused.
//
// The DeleteType mapping mirrors DeleteItem: HardDelete and SoftDelete both move
// the items to the recoverable-items dumpster (hermEX keeps a recoverable copy
// even on a hard delete, the same safety net the rest of the system applies, and
// Exchange likewise retains hard-deleted items in the dumpster until retention
// expires); MoveToDeletedItems moves them to the Deleted Items folder. v1 empties
// only the caller's own mailbox; a delegated or public-store target is refused.

type emptyFolderRequest struct {
	DeleteType       string     `xml:"DeleteType,attr"`
	DeleteSubFolders bool       `xml:"DeleteSubFolders,attr"`
	FolderIDs        folderRefs `xml:"FolderIds"`
}

type emptyFolderResponse struct {
	XMLName  xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages EmptyFolderResponse"`
	Messages []folderResponseMessage `xml:"ResponseMessages>EmptyFolderResponseMessage"`
}

// handleEmptyFolder answers EmptyFolder: each named folder is emptied of its items
// (and, when DeleteSubFolders is set, its subfolders are deleted with it).
func (s *Server) handleEmptyFolder(w http.ResponseWriter, inner []byte, sess *session) {
	var req emptyFolderRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "EmptyFolder: "+err.Error())
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	var msgs []folderResponseMessage
	for _, tgt := range resolveTargets(req.FolderIDs) {
		msgs = append(msgs, emptyOneFolder(st, tgt, req.DeleteType, req.DeleteSubFolders))
	}
	writeResponse(w, emptyFolderResponse{Messages: msgs})
}

// emptyOneFolder empties one resolved folder, refusing a target that is not a
// real folder in the caller's own mailbox.
func emptyOneFolder(st *objectstore.Store, tgt folderTarget, deleteType string, subfolders bool) folderResponseMessage {
	if !tgt.ok {
		code := tgt.code
		if code == "" {
			code = "ErrorFolderNotFound"
		}
		return folderError(code)
	}
	if tgt.mailbox != "" {
		// A delegated mailbox or the public store: v1 empties only the caller's own.
		return folderError("ErrorAccessDenied")
	}
	if err := emptyFolderItems(st, tgt.fid, deleteType); err != nil {
		return folderError("ErrorItemNotFound")
	}
	if subfolders {
		if err := deleteSubfolders(st, tgt.fid); err != nil {
			return folderError("ErrorCannotDeleteObject")
		}
	}
	return folderResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"}
}

// emptyFolderItems applies the DeleteType disposition to every item in the folder.
func emptyFolderItems(st *objectstore.Store, fid int64, deleteType string) error {
	items, err := st.ListMessages(fid)
	if err != nil {
		return err
	}
	for _, m := range items {
		var derr error
		switch deleteType {
		case "MoveToDeletedItems":
			_, derr = moveMessage(st, fid, m.UID, int64(mapi.PrivateFIDDeletedItems))
		default: // HardDelete, SoftDelete, or unspecified: to the recoverable dumpster
			derr = st.SoftDeleteMessage(fid, m.UID)
		}
		if derr != nil {
			return derr
		}
	}
	return nil
}

// deleteSubfolders removes the target folder's direct children; DeleteFolder
// cascades each child's whole subtree, so the folder's descendants are removed
// without a manual recursion.
func deleteSubfolders(st *objectstore.Store, parentFID int64) error {
	all, err := st.ListFolders()
	if err != nil {
		return err
	}
	for _, f := range all {
		if f.ParentID != nil && *f.ParentID == parentFID {
			if err := st.DeleteFolder(f.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
