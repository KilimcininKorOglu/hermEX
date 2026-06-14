package ews

import (
	"encoding/xml"
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
	if len(targets) == 0 || !targets[0].ok {
		writeResponse(w, createFolderResponse{Messages: []folderResponseMessage{folderError("ErrorInvalidRequest")}})
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

// folderError builds an error folder response message.
func folderError(code string) folderResponseMessage {
	return folderResponseMessage{ResponseClass: "Error", ResponseCode: code}
}
