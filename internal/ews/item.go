package ews

import (
	"encoding/xml"
	"net/http"
	"net/mail"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// --- request types ---

type getItemRequest struct {
	ItemIDs struct {
		Items []refID `xml:"ItemId"`
	} `xml:"ItemIds"`
}

type findItemRequest struct {
	Traversal       string     `xml:"Traversal,attr"`
	ParentFolderIDs folderRefs `xml:"ParentFolderIds"`
}

type getAttachmentRequest struct {
	AttachmentIDs struct {
		IDs []refID `xml:"AttachmentId"`
	} `xml:"AttachmentIds"`
}

// --- response types ---

type getItemResponse struct {
	XMLName  xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetItemResponse"`
	Messages []itemResponseMessage `xml:"ResponseMessages>GetItemResponseMessage"`
}

type findItemResponse struct {
	XMLName  xml.Name                  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindItemResponse"`
	Messages []findItemResponseMessage `xml:"ResponseMessages>FindItemResponseMessage"`
}

type getAttachmentResponse struct {
	XMLName  xml.Name                       `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetAttachmentResponse"`
	Messages []getAttachmentResponseMessage `xml:"ResponseMessages>GetAttachmentResponseMessage"`
}

type itemResponseMessage struct {
	ResponseClass string     `xml:"ResponseClass,attr"`
	ResponseCode  string     `xml:"ResponseCode"`
	Items         *itemsWrap `xml:"Items,omitempty"`
}

type findItemResponseMessage struct {
	ResponseClass string        `xml:"ResponseClass,attr"`
	ResponseCode  string        `xml:"ResponseCode"`
	RootFolder    *findItemRoot `xml:"RootFolder,omitempty"`
}

type getAttachmentResponseMessage struct {
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  string           `xml:"ResponseCode"`
	Attachments   *attachmentsWrap `xml:"Attachments,omitempty"`
}

// itemsWrap holds an <Items> list; each child is an oxews.Message carrying its
// own types-namespace element name.
type itemsWrap struct {
	Messages []oxews.Message
}

type findItemRoot struct {
	TotalItemsInView        int  `xml:"TotalItemsInView,attr"`
	IncludesLastItemInRange bool `xml:"IncludesLastItemInRange,attr"`
	// In Find* responses the collection under m:RootFolder is in the types
	// namespace (t:Items), unlike the messages-namespace m:Items of GetItem.
	Items itemsWrap `xml:"http://schemas.microsoft.com/exchange/services/2006/types Items"`
}

// attachmentsWrap holds an <Attachments> list of types-namespace File and Item
// attachments.
type attachmentsWrap struct {
	Files []oxews.FileAttachment `xml:"http://schemas.microsoft.com/exchange/services/2006/types FileAttachment"`
	Items []oxews.ItemAttachment `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemAttachment"`
}

// --- handlers ---

// handleFindItem answers FindItem: it lists each parent folder's messages as
// summary items (no body, index-projected fields).
func (s *Server) handleFindItem(w http.ResponseWriter, inner []byte, sess *session) {
	var req findItemRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "FindItem: "+err.Error())
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()

	var msgs []findItemResponseMessage
	for _, tgt := range resolveTargets(req.ParentFolderIDs) {
		if !tgt.ok {
			msgs = append(msgs, findItemResponseMessage{ResponseClass: "Error", ResponseCode: tgt.code})
			continue
		}
		st, _, isOwn, code := cache.open(sess, tgt.mailbox)
		if code != "" {
			msgs = append(msgs, findItemResponseMessage{ResponseClass: "Error", ResponseCode: code})
			continue
		}
		// Listing another mailbox's folder requires only folder visibility; reading an
		// item's content (GetItem) is separately gated on read access. This two-tier
		// model matches the EWS enforcement contract.
		if !isOwn {
			rights, err := st.ResolvePermission(tgt.fid, sess.user)
			if err != nil {
				msgs = append(msgs, findItemResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInternalServerError"})
				continue
			}
			if rights&mapi.FrightsVisible == 0 {
				msgs = append(msgs, findItemResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorAccessDenied"})
				continue
			}
		}
		items, err := st.ListMessages(tgt.fid)
		if err != nil {
			msgs = append(msgs, findItemResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorItemNotFound"})
			continue
		}
		idMailbox := ""
		if !isOwn {
			idMailbox = tgt.mailbox
		}
		elems := make([]oxews.Message, 0, len(items))
		for _, info := range items {
			elems = append(elems, itemSummary(st, tgt.fid, info, idMailbox))
		}
		msgs = append(msgs, findItemResponseMessage{
			ResponseClass: "Success", ResponseCode: "NoError",
			RootFolder: &findItemRoot{
				TotalItemsInView:        len(elems),
				IncludesLastItemInRange: true,
				Items:                   itemsWrap{Messages: elems},
			},
		})
	}
	writeResponse(w, findItemResponse{Messages: msgs})
}

// handleGetItem answers GetItem: each requested item is opened, its body read
// from the message's RFC822 form (the store keeps no HTML body property), and
// rendered as a full <t:Message>.
func (s *Server) handleGetItem(w http.ResponseWriter, inner []byte, sess *session) {
	var req getItemRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetItem: "+err.Error())
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()

	var msgs []itemResponseMessage
	for _, ref := range req.ItemIDs.Items {
		id, err := oxews.DecodeItemID(ref.ID)
		if err != nil {
			msgs = append(msgs, itemResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInvalidRequest"})
			continue
		}
		// The id self-encodes its mailbox: an own-mailbox id (empty) opens the caller's
		// store, a delegated id opens the target and is gated on the caller's read access.
		st, _, isOwn, code := cache.open(sess, id.Mailbox)
		if code != "" {
			msgs = append(msgs, itemResponseMessage{ResponseClass: "Error", ResponseCode: code})
			continue
		}
		if !isOwn {
			rights, err := st.ResolvePermission(id.FolderID, sess.user)
			if err != nil {
				msgs = append(msgs, itemResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInternalServerError"})
				continue
			}
			if rights&mapi.FrightsReadAny == 0 {
				msgs = append(msgs, itemResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorAccessDenied"})
				continue
			}
		}
		msg, err := st.OpenMessage(id.MessageID)
		if err != nil {
			msgs = append(msgs, itemResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorItemNotFound"})
			continue
		}
		info, _ := st.MessageByUID(id.FolderID, id.UID)
		hasAttach, _ := st.HasAttachments(id.MessageID)
		body, bodyType := "", "Text"
		if raw, err := st.GetMessageRaw(id.FolderID, id.UID); err == nil {
			body, bodyType = bodyFromRaw(raw)
		}
		elem := oxews.BuildItem(msg, oxews.ItemMeta{
			ItemID:         ref.ID,
			FolderID:       id.FolderID,
			MessageID:      id.MessageID,
			Mailbox:        id.Mailbox,
			ChangeKey:      oxews.ChangeKey(uint64(id.MessageID)),
			IsRead:         info.Flags&objectstore.FlagSeen != 0,
			HasAttachments: hasAttach,
			Received:       info.InternalDate,
			Size:           int(info.Size),
			Body:           body,
			BodyType:       bodyType,
		})
		msgs = append(msgs, itemResponseMessage{
			ResponseClass: "Success", ResponseCode: "NoError",
			Items: &itemsWrap{Messages: []oxews.Message{elem}},
		})
	}
	writeResponse(w, getItemResponse{Messages: msgs})
}

// handleGetAttachment answers GetAttachment: each attachment id is resolved to
// its message and index, and the full FileAttachment (with base64 content) is
// returned.
func (s *Server) handleGetAttachment(w http.ResponseWriter, inner []byte, sess *session) {
	var req getAttachmentRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetAttachment: "+err.Error())
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()

	var msgs []getAttachmentResponseMessage
	for _, ref := range req.AttachmentIDs.IDs {
		folderID, mid, idx, mailbox, err := oxews.DecodeAttachmentID(ref.ID)
		if err != nil {
			msgs = append(msgs, getAttachmentResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInvalidRequest"})
			continue
		}
		// The id self-encodes its mailbox and parent folder; a delegated attachment is
		// gated on read access to that folder (reference: GetAttachment checks
		// frightsReadAny on the attachment's parent folder).
		st, _, isOwn, code := cache.open(sess, mailbox)
		if code != "" {
			msgs = append(msgs, getAttachmentResponseMessage{ResponseClass: "Error", ResponseCode: code})
			continue
		}
		if !isOwn {
			rights, err := st.ResolvePermission(folderID, sess.user)
			if err != nil {
				msgs = append(msgs, getAttachmentResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInternalServerError"})
				continue
			}
			if rights&mapi.FrightsReadAny == 0 {
				msgs = append(msgs, getAttachmentResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorAccessDenied"})
				continue
			}
		}
		msg, err := st.OpenMessage(mid)
		if err != nil || idx < 0 || idx >= len(msg.Attachments) {
			msgs = append(msgs, getAttachmentResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorItemNotFound"})
			continue
		}
		att := msg.Attachments[idx]
		if oxews.IsEmbeddedAttachment(att) {
			// An embedded message is returned as an ItemAttachment carrying the nested
			// message item, not a file blob.
			ia := oxews.BuildItemAttachmentContent(folderID, mid, idx, att, mailbox)
			msgs = append(msgs, getAttachmentResponseMessage{
				ResponseClass: "Success", ResponseCode: "NoError",
				Attachments: &attachmentsWrap{Items: []oxews.ItemAttachment{ia}},
			})
			continue
		}
		fa := oxews.BuildAttachmentContent(folderID, mid, idx, att, mailbox)
		msgs = append(msgs, getAttachmentResponseMessage{
			ResponseClass: "Success", ResponseCode: "NoError",
			Attachments: &attachmentsWrap{Files: []oxews.FileAttachment{fa}},
		})
	}
	writeResponse(w, getAttachmentResponse{Messages: msgs})
}

// --- helpers ---

// bodyFromRaw extracts the displayable body from a message's RFC822 bytes,
// preferring HTML over plain text. The store keeps no HTML body property, so the
// body is read from the MIME structure (the webmail reader's proven path).
func bodyFromRaw(raw []byte) (content, bodyType string) {
	root := mime.ParseStructure(raw)
	if p := findBodyPart(root, "html"); p != nil {
		if txt, err := p.DecodedText(); err == nil {
			return txt, "HTML"
		}
	}
	if p := findBodyPart(root, "plain"); p != nil {
		if txt, err := p.DecodedText(); err == nil {
			return txt, "Text"
		}
	}
	return "", "Text"
}

// findBodyPart finds the first non-attachment text part of the given subtype.
func findBodyPart(p *mime.Part, subtype string) *mime.Part {
	if p == nil {
		return nil
	}
	if p.Type == "text" && p.Subtype == subtype && p.Disposition != "attachment" {
		return p
	}
	for _, c := range p.Children {
		if r := findBodyPart(c, subtype); r != nil {
			return r
		}
	}
	return nil
}

// itemSummary builds a summary <t:Message> for an indexed message in the given
// folder, shared by FindItem and SyncFolderItems. mailbox is the target mailbox SMTP
// when the folder lives in another mailbox (so the minted item id reopens it later);
// empty for the caller's own mailbox.
func itemSummary(st *objectstore.Store, folderID int64, info objectstore.MessageInfo, mailbox string) oxews.Message {
	hasAttach, _ := st.HasAttachments(info.ID)
	name, email := splitAddress(info.Sender)
	return oxews.BuildSummary(oxews.SummaryMeta{
		ItemID:         oxews.EncodeItemID(oxews.ItemID{FolderID: folderID, MessageID: info.ID, UID: info.UID, Mailbox: mailbox}),
		ChangeKey:      oxews.ChangeKey(uint64(info.ID)),
		Subject:        info.Subject,
		SenderName:     name,
		SenderEmail:    email,
		Received:       info.InternalDate,
		Size:           int(info.Size),
		IsRead:         info.Flags&objectstore.FlagSeen != 0,
		HasAttachments: hasAttach,
	})
}

// splitAddress splits a formatted originator ("Name <addr>") into name + email.
func splitAddress(s string) (name, email string) {
	if s == "" {
		return "", ""
	}
	if a, err := mail.ParseAddress(s); err == nil {
		return a.Name, a.Address
	}
	// ParseAddress rejects a display name that is itself a bare address (an
	// unquoted '@', which the index emits for a sender with no display name, e.g.
	// "ops@x <ops@x>"); fall back to the angle-addr so the summary carries a clean
	// EmailAddress rather than the whole malformed string.
	if i := strings.LastIndex(s, "<"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j > 1 {
			return "", strings.TrimSpace(s[i+1 : i+j])
		}
	}
	return "", s
}
