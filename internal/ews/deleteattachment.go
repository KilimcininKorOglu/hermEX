package ews

import (
	"encoding/xml"
	"net/http"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// Attachment deletion (MS-OXWSATT DeleteAttachment) removes one or more attachments
// from a message and returns the parent item's id so the client can refresh it.
// The AttachmentId encodes the parent (folder, message) and the attachment's
// position; the position is resolved to the attachment's stable
// PidTagAttachNumber, which the store deletes by (so removing one attachment never
// renumbers the others). Like CreateAttachment, the operation is gated on edit
// access to the parent folder, so a delegated item is refused without the right.

type deleteAttachmentRequest struct {
	AttachmentIDs struct {
		IDs []refID `xml:"AttachmentId"`
	} `xml:"AttachmentIds"`
}

type deleteAttachmentResponse struct {
	XMLName  xml.Name                          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteAttachmentResponse"`
	Messages []deleteAttachmentResponseMessage `xml:"ResponseMessages>DeleteAttachmentResponseMessage"`
}

type deleteAttachmentResponseMessage struct {
	ResponseClass string      `xml:"ResponseClass,attr"`
	ResponseCode  string      `xml:"ResponseCode"`
	RootItemID    *rootItemID `xml:"RootItemId,omitempty"`
}

// rootItemID is the parent item the attachment was removed from. The element name
// and its first attribute are both "RootItemId" (MS-OXWSATT 2.2.4.4).
type rootItemID struct {
	RootItemID        string `xml:"RootItemId,attr"`
	RootItemChangeKey string `xml:"RootItemChangeKey,attr"`
}

// handleDeleteAttachment answers DeleteAttachment: each named attachment is removed
// from its parent message, and the parent's id is returned.
func (s *Server) handleDeleteAttachment(w http.ResponseWriter, inner []byte, sess *session) {
	var req deleteAttachmentRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "DeleteAttachment: "+err.Error())
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()

	var msgs []deleteAttachmentResponseMessage
	for _, ref := range req.AttachmentIDs.IDs {
		msgs = append(msgs, s.deleteOneAttachment(cache, sess, ref.ID))
	}
	writeResponse(w, deleteAttachmentResponse{Messages: msgs})
}

// deleteOneAttachment removes a single attachment named by its EWS id.
func (s *Server) deleteOneAttachment(cache *storeCache, sess *session, attachID string) deleteAttachmentResponseMessage {
	folderID, mid, idx, mailbox, err := oxews.DecodeAttachmentID(attachID)
	if err != nil {
		return deleteAttachmentErr("ErrorInvalidRequest")
	}
	// The id self-encodes its mailbox and parent folder; removing an attachment edits
	// the parent message, so a delegated item is gated on edit access to its folder.
	st, code := cache.openForItem(sess, oxews.ItemID{FolderID: folderID, MessageID: mid, Mailbox: mailbox}, mapi.FrightsEditAny)
	if code != "" {
		return deleteAttachmentErr(code)
	}

	msg, err := st.OpenMessage(mid)
	if err != nil || idx < 0 || idx >= len(msg.Attachments) {
		return deleteAttachmentErr("ErrorItemNotFound")
	}
	attNum, ok := attachNumber(msg.Attachments[idx])
	if !ok {
		return deleteAttachmentErr("ErrorItemNotFound")
	}
	if err := st.DeleteAttachment(mid, attNum); err != nil {
		return deleteAttachmentErr("ErrorItemNotFound")
	}
	// Advance the parent's modification time so content sync observes the change,
	// matching CreateAttachment's re-save.
	_ = st.ModifyMessageProperties(mid, mapi.PropertyValues{
		{Tag: mapi.PrLastModificationTime, Value: mapi.UnixToNTTime(time.Now())},
	})

	uid, _ := messageUID(st, folderID, mid)
	rootID := oxews.EncodeItemID(oxews.ItemID{FolderID: folderID, MessageID: mid, UID: uid, Mailbox: mailbox})
	return deleteAttachmentResponseMessage{
		ResponseClass: "Success",
		ResponseCode:  "NoError",
		RootItemID:    &rootItemID{RootItemID: rootID, RootItemChangeKey: oxews.ChangeKey(uint64(mid))},
	}
}

// deleteAttachmentErr builds an error response message.
func deleteAttachmentErr(code string) deleteAttachmentResponseMessage {
	return deleteAttachmentResponseMessage{ResponseClass: "Error", ResponseCode: code}
}

// attachNumber reads an attachment's stable PidTagAttachNumber.
func attachNumber(att oxcmail.Attachment) (uint32, bool) {
	v, ok := att.Props.Get(mapi.PrAttachNum)
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int32:
		return uint32(n), true
	case int64:
		return uint32(n), true
	case int:
		return uint32(n), true
	default:
		return 0, false
	}
}

// messageUID resolves a message's IMAP uid within its folder, for building the
// parent item id; ok is false when the message is no longer listable.
func messageUID(st *objectstore.Store, folderID, messageID int64) (uint32, bool) {
	msgs, err := st.ListMessages(folderID)
	if err != nil {
		return 0, false
	}
	for _, m := range msgs {
		if m.ID == messageID {
			return m.UID, true
		}
	}
	return 0, false
}
