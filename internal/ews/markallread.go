package ews

import (
	"encoding/xml"
	"net/http"

	"hermex/internal/objectstore"
)

// Bulk read-flagging (MS-OXWSCORE MarkAllItemsAsRead) sets or clears the read flag
// on every item in one or more folders. ReadFlag true marks them read, false marks
// them unread. Each message it takes from unread to read sends the read receipt it
// asks for, unless SuppressReadReceipts is true, as Exchange does. v1 marks only
// the caller's own mailbox; a delegated or public-store target is refused.

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
		s.soapFault(w, "ErrorInvalidRequest", "MarkAllItemsAsRead: invalid request", err)
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		s.soapFault(w, "ErrorInternalServerError", "an internal error occurred", err)
		return
	}
	defer st.Close()

	var msgs []folderResponseMessage
	for _, tgt := range resolveTargets(req.FolderIDs) {
		rm, becameRead := markFolderRead(st, tgt, req.ReadFlag)
		msgs = append(msgs, rm)
		if req.SuppressReadReceipts {
			continue
		}
		for _, id := range becameRead {
			s.sendReadReceipt(st, sess, "", id)
		}
	}
	writeResponse(w, markAllReadResponse{Messages: msgs})
}

// markFolderRead sets the read flag on every item in one resolved folder. becameRead
// lists the messages it took from unread to read, which are the ones that owe a
// read receipt.
func markFolderRead(st *objectstore.Store, tgt folderTarget, read bool) (rm folderResponseMessage, becameRead []int64) {
	if !tgt.ok {
		code := tgt.code
		if code == "" {
			code = "ErrorFolderNotFound"
		}
		return folderError(code), nil
	}
	if tgt.mailbox != "" {
		return folderError("ErrorAccessDenied"), nil
	}
	items, err := st.ListMessages(tgt.fid)
	if err != nil {
		return folderError("ErrorItemNotFound"), nil
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
			return folderError("ErrorItemNotFound"), becameRead
		}
		if read {
			becameRead = append(becameRead, m.ID)
		}
	}
	return folderResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"}, becameRead
}
