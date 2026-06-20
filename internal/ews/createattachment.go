package ews

import (
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// createAttachmentRequest is the MS-OXWSATT CreateAttachment request: a parent
// item id plus the attachments to add. Only FileAttachment is consumed (matching
// the reference); an ItemAttachment on input is reported unsupported.
type createAttachmentRequest struct {
	ParentItemID refID `xml:"ParentItemId"`
	Attachments  struct {
		Files []reqFileAttachment `xml:"FileAttachment"`
		Items []struct{}          `xml:"ItemAttachment"`
	} `xml:"Attachments"`
}

// reqFileAttachment is the subset of <t:FileAttachment> CreateAttachment reads.
// ContentType and ContentId are set so a later GetAttachment round-trips them
// (the store's GetAttachment reads Pr_AttachMimeTag/Pr_AttachContentId back).
type reqFileAttachment struct {
	Name        string `xml:"Name"`
	ContentType string `xml:"ContentType"`
	ContentID   string `xml:"ContentId"`
	IsInline    bool   `xml:"IsInline"`
	Content     string `xml:"Content"` // base64
}

type createAttachmentResponse struct {
	XMLName  xml.Name                          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateAttachmentResponse"`
	Messages []createAttachmentResponseMessage `xml:"ResponseMessages>CreateAttachmentResponseMessage"`
}

type createAttachmentResponseMessage struct {
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  string           `xml:"ResponseCode"`
	Attachments   *attachmentsWrap `xml:"Attachments,omitempty"`
}

// handleCreateAttachment answers CreateAttachment: each FileAttachment is written
// to the parent message as a single by-value attachment, and its new
// AttachmentId is returned (one CreateAttachmentResponseMessage per attachment,
// per MS-OXWSATT 3.1.4.1). The id encodes (message, position): a new attachment
// is always last in the message's attachment_id order (AUTOINCREMENT + the
// ORDER BY in OpenMessage), so its position is the message's prior attachment
// count plus the number created so far in this request — the same positional
// scheme GetAttachment decodes. After writing, the parent's change number is
// advanced (matching the reference's re-save) so ICS content sync observes the
// new attachment. Operates on the requester's own mailbox, as GetAttachment does.
//
// Deviations / v1 gaps: ItemAttachment input (an embedded item) is not supported;
// the ParentItemId ChangeKey is ignored (no optimistic-concurrency check); and the
// positional id is not stable across deletion of a lower-positioned attachment —
// the whole attachment surface (GetAttachment/DeleteAttachment) decodes positions,
// so a stable attach-number id cannot be introduced here in isolation.
func (s *Server) handleCreateAttachment(w http.ResponseWriter, inner []byte, sess *session) {
	var req createAttachmentRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "CreateAttachment: "+err.Error())
		return
	}

	total := len(req.Attachments.Files) + len(req.Attachments.Items)
	if total == 0 {
		writeResponse(w, createAttachmentResponse{})
		return
	}

	// errAll fails every requested attachment with one shared response code (used
	// when the parent itself is unusable).
	errAll := func(code string) {
		msgs := make([]createAttachmentResponseMessage, total)
		for i := range msgs {
			msgs[i] = createAttachmentResponseMessage{ResponseClass: "Error", ResponseCode: code}
		}
		writeResponse(w, createAttachmentResponse{Messages: msgs})
	}

	parent, err := oxews.DecodeItemID(req.ParentItemID.ID)
	if err != nil {
		errAll("ErrorInvalidId")
		return
	}
	if parent.Mailbox != "" {
		// Attaching to another mailbox's item is not yet supported; reject rather than
		// operate on the caller's own store with a foreign id.
		errAll("ErrorAccessDenied")
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	msg, err := st.OpenMessage(parent.MessageID)
	if err != nil {
		errAll("ErrorItemNotFound")
		return
	}
	baseCount := len(msg.Attachments)

	var msgs []createAttachmentResponseMessage
	created := 0
	for _, fa := range req.Attachments.Files {
		content, err := base64.StdEncoding.DecodeString(fa.Content)
		if err != nil {
			msgs = append(msgs, createAttachmentResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInvalidRequest"})
			continue
		}
		now := mapi.UnixToNTTime(time.Now())
		props := mapi.PropertyValues{
			{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
			{Tag: mapi.PrRenderingPosition, Value: int32(-1)}, // 0xFFFFFFFF: not body-rendered
			{Tag: mapi.PrCreationTime, Value: now},
			{Tag: mapi.PrLastModificationTime, Value: now},
			{Tag: mapi.PrAttachLongFilename, Value: fa.Name},
			{Tag: mapi.PrAttachFilename, Value: fa.Name},
			{Tag: mapi.PrDisplayName, Value: fa.Name},
			{Tag: mapi.PrAttachDataBin, Value: content},
		}
		if fa.ContentType != "" {
			props.Set(mapi.PrAttachMimeTag, fa.ContentType)
		}
		if fa.ContentID != "" {
			props.Set(mapi.PrAttachContentID, fa.ContentID)
		}
		if fa.IsInline {
			props.Set(mapi.PrAttachFlags, int32(mapi.AttMhtmlRef))
		}
		if _, _, err := st.CreateAttachment(parent.MessageID, props); err != nil {
			msgs = append(msgs, createAttachmentResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorItemSave"})
			continue
		}
		id := oxews.EncodeAttachmentID(parent.FolderID, parent.MessageID, baseCount+created, parent.Mailbox)
		created++
		msgs = append(msgs, createAttachmentResponseMessage{
			ResponseClass: "Success", ResponseCode: "NoError",
			Attachments: &attachmentsWrap{Files: []oxews.FileAttachment{{AttachmentID: oxews.AttachmentIDElem{ID: id}}}},
		})
	}
	for range req.Attachments.Items {
		msgs = append(msgs, createAttachmentResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInvalidRequest"})
	}

	if created > 0 {
		if err := st.ModifyMessageProperties(parent.MessageID, mapi.PropertyValues{
			{Tag: mapi.PrLastModificationTime, Value: mapi.UnixToNTTime(time.Now())},
		}); err != nil {
			writeSOAPFault(w, "ErrorInternalServerError", err.Error())
			return
		}
	}
	writeResponse(w, createAttachmentResponse{Messages: msgs})
}
