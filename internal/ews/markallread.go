package ews

import (
	"encoding/xml"
	"net/http"

	"hermex/internal/objectstore"
)

// Bulk read-flagging (MS-OXWSCORE MarkAllItemsAsRead) sets or clears the read flag
// on every item in one or more folders. ReadFlag true marks them read, false marks
// them unread. SuppressReadReceipts is accepted but always effectively honored:
// hermEX never emits a read receipt on a bulk flag change, so no receipt is sent
// regardless. v1 marks only the caller's own mailbox; a delegated or public-store
// target is refused.

type markAllReadRequest struct {
	ReadFlag             bool       `xml:"ReadFlag"`
	SuppressReadReceipts bool       `xml:"SuppressReadReceipts"`
	FolderIDs            folderRefs `xml:"FolderIds"`
}

type markAllReadResponse struct {
	XMLName  xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MarkAllItemsAsReadResponse"`
	Messages []folderResponseMessage `xml:"ResponseMessages>MarkAllItemsAsReadResponseMessage"`
}

// handleMarkAllItemsAsRead answers MarkAllItemsAsRead: every item in each named
// folder has its read flag set to ReadFlag.
func (s *Server) handleMarkAllItemsAsRead(w http.ResponseWriter, inner []byte, sess *session) {
	var req markAllReadRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "MarkAllItemsAsRead: "+err.Error())
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
		msgs = append(msgs, markFolderRead(st, tgt, req.ReadFlag))
	}
	writeResponse(w, markAllReadResponse{Messages: msgs})
}

// markFolderRead sets the read flag on every item in one resolved folder.
func markFolderRead(st *objectstore.Store, tgt folderTarget, read bool) folderResponseMessage {
	if !tgt.ok {
		code := tgt.code
		if code == "" {
			code = "ErrorFolderNotFound"
		}
		return folderError(code)
	}
	if tgt.mailbox != "" {
		return folderError("ErrorAccessDenied")
	}
	items, err := st.ListMessages(tgt.fid)
	if err != nil {
		return folderError("ErrorItemNotFound")
	}
	for _, m := range items {
		next := m.Flags
		if read {
			next |= objectstore.FlagSeen
		} else {
			next &^= objectstore.FlagSeen
		}
		if next == m.Flags {
			continue
		}
		if err := st.SetMessageFlags(tgt.fid, m.UID, next); err != nil {
			return folderError("ErrorItemNotFound")
		}
	}
	return folderResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"}
}
