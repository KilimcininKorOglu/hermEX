package ews

import (
	"encoding/xml"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
	"hermex/internal/oxtask"
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

// itemsWrap holds an <Items> list; each child carries its own types-namespace
// element name, so a folder of tasks serializes as <t:Task> and a folder of mail as
// <t:Message>.
type itemsWrap struct {
	Messages  []oxews.Message
	Tasks     []oxews.Task
	BaseItems []oxews.Item
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
		// A recoverable (Recoverable Items dumpster) target is intentionally ok=false so
		// every other handler still reports ErrorFolderNotFound; FindItem serves it.
		if !tgt.ok && !tgt.recoverable {
			msgs = append(msgs, findItemResponseMessage{ResponseClass: "Error", ResponseCode: tgt.code})
			continue
		}
		if tgt.public {
			// The public folders root is a container holding no items of its own; its
			// public child folders carry the items and are addressed by their own ids.
			msgs = append(msgs, findItemResponseMessage{
				ResponseClass: "Success", ResponseCode: "NoError",
				RootFolder: &findItemRoot{IncludesLastItemInRange: true},
			})
			continue
		}
		if tgt.recoverable {
			// The Recoverable Items dumpster aggregates soft-deleted items mailbox-wide,
			// so it is served only for the caller's own mailbox (no per-folder ACL applies
			// to an aggregate). Each item keeps its original parent folder in its id.
			st, _, isOwn, code := cache.open(sess, tgt.mailbox)
			if code != "" {
				msgs = append(msgs, findItemResponseMessage{ResponseClass: "Error", ResponseCode: code})
				continue
			}
			if !isOwn {
				msgs = append(msgs, findItemResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorAccessDenied"})
				continue
			}
			items, err := st.ListAllSoftDeleted()
			if err != nil {
				msgs = append(msgs, findItemResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInternalServerError"})
				continue
			}
			elems := make([]oxews.Message, 0, len(items))
			for _, it := range items {
				elems = append(elems, itemSummary(st, it.FolderID, it.Info, ""))
			}
			msgs = append(msgs, findItemResponseMessage{
				ResponseClass: "Success", ResponseCode: "NoError",
				RootFolder: &findItemRoot{
					TotalItemsInView:        len(elems),
					IncludesLastItemInRange: true,
					Items:                   itemsWrap{Messages: elems},
				},
			})
			continue
		}
		st, _, isOwn, code := cache.open(sess, tgt.mailbox)
		if code == codePublicAbsent {
			code = "ErrorFolderNotFound" // a public folder whose domain store is gone
		}
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
		if tgt.fid == int64(mapi.PrivateFIDTasks) {
			// Tasks live in the object store (versioned by change number), not the IMAP
			// index, so they are listed as folder objects rather than messages.
			objs, _ := st.ListFolderObjects(tgt.fid)
			tasks := make([]oxews.Task, 0, len(objs))
			for _, o := range objs {
				tasks = append(tasks, taskSummary(st, tgt.fid, o.ID, idMailbox))
			}
			msgs = append(msgs, findItemResponseMessage{
				ResponseClass: "Success", ResponseCode: "NoError",
				RootFolder: &findItemRoot{
					TotalItemsInView:        len(tasks),
					IncludesLastItemInRange: true,
					Items:                   itemsWrap{Tasks: tasks},
				},
			})
			continue
		}
		if tgt.fid == int64(mapi.PrivateFIDNotes) {
			objs, _ := st.ListFolderObjects(tgt.fid)
			notes := make([]oxews.Item, 0, len(objs))
			for _, o := range objs {
				notes = append(notes, noteSummary(st, tgt.fid, o.ID, idMailbox))
			}
			msgs = append(msgs, findItemResponseMessage{
				ResponseClass: "Success", ResponseCode: "NoError",
				RootFolder: &findItemRoot{
					TotalItemsInView:        len(notes),
					IncludesLastItemInRange: true,
					Items:                   itemsWrap{BaseItems: notes},
				},
			})
			continue
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
		if code == codePublicAbsent {
			code = "ErrorItemNotFound" // a public item whose domain store is gone
		}
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
		// A task is rendered as <t:Task> from its shared properties, not the mail
		// MIME path (a task has no RFC822 form).
		if itemClass(msg.Props) == oxtask.MessageClass {
			tk, _ := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
			elem := oxews.BuildTask(tk, oxews.ItemMeta{
				ItemID:         ref.ID,
				ChangeKey:      oxews.ChangeKey(uint64(id.MessageID)),
				HasAttachments: hasAttach,
			})
			msgs = append(msgs, itemResponseMessage{
				ResponseClass: "Success", ResponseCode: "NoError",
				Items: &itemsWrap{Tasks: []oxews.Task{elem}},
			})
			continue
		}
		// A sticky note is rendered as a base <t:Item> (EWS has no Note type) from its
		// shared properties.
		if itemClass(msg.Props) == oxews.NoteClass {
			elem := buildNoteItem(st, msg.Props, ref.ID, oxews.ChangeKey(uint64(id.MessageID)))
			msgs = append(msgs, itemResponseMessage{
				ResponseClass: "Success", ResponseCode: "NoError",
				Items: &itemsWrap{BaseItems: []oxews.Item{elem}},
			})
			continue
		}
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
		if code == codePublicAbsent {
			code = "ErrorItemNotFound" // a public attachment whose domain store is gone
		}
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

// itemClass returns a stored message's class.
func itemClass(props mapi.PropertyValues) string {
	if v, ok := props.Get(mapi.PrMessageClass); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// taskSummary builds a <t:Task> for FindItem on the Tasks folder from a stored
// object. The folder is small, so reading each task's shared properties for a
// complete summary is acceptable.
func taskSummary(st *objectstore.Store, folderID, objectID int64, mailbox string) oxews.Task {
	meta := oxews.ItemMeta{
		ItemID:    oxews.EncodeItemID(oxews.ItemID{FolderID: folderID, MessageID: objectID, Mailbox: mailbox}),
		ChangeKey: oxews.ChangeKey(uint64(objectID)),
	}
	if msg, err := st.OpenMessage(objectID); err == nil {
		tk, _ := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
		return oxews.BuildTask(tk, meta)
	}
	return oxews.Task{ItemID: oxews.ItemIDElem{ID: meta.ItemID, ChangeKey: meta.ChangeKey}}
}

// buildNoteItem renders a stored sticky note as an EWS base <t:Item>.
func buildNoteItem(st *objectstore.Store, props mapi.PropertyValues, itemID, changeKey string) oxews.Item {
	return oxews.BuildNote(
		oxews.ItemMeta{ItemID: itemID, ChangeKey: changeKey},
		strProp(props, mapi.PrSubject),
		strProp(props, mapi.PrBody),
		noteKeywords(st, props),
		ntTime(props, mapi.PrLastModificationTime),
	)
}

// noteSummary builds a <t:Item> for FindItem on the Notes folder from a stored object.
func noteSummary(st *objectstore.Store, folderID, objectID int64, mailbox string) oxews.Item {
	itemID := oxews.EncodeItemID(oxews.ItemID{FolderID: folderID, MessageID: objectID, Mailbox: mailbox})
	changeKey := oxews.ChangeKey(uint64(objectID))
	if msg, err := st.OpenMessage(objectID); err == nil {
		return buildNoteItem(st, msg.Props, itemID, changeKey)
	}
	return oxews.Item{ItemID: oxews.ItemIDElem{ID: itemID, ChangeKey: changeKey}, ItemClass: oxews.NoteClass}
}

// strProp reads a string (or []byte text) property as a string.
func strProp(props mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := props.Get(tag); ok {
		switch s := v.(type) {
		case string:
			return s
		case []byte:
			return string(s)
		}
	}
	return ""
}

// ntTime reads a PtSysTime property as a UTC time, or the zero time when absent.
func ntTime(props mapi.PropertyValues, tag mapi.PropTag) time.Time {
	if v, ok := props.Get(tag); ok {
		if nt, ok := v.(uint64); ok {
			return mapi.NTTimeToUnix(nt).UTC()
		}
	}
	return time.Time{}
}

// noteKeywords reads a message's category keywords (the shared multivalue named
// property).
func noteKeywords(st *objectstore.Store, props mapi.PropertyValues) []string {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameKeywords})
	if err != nil || ids[0] == 0 {
		return nil
	}
	if v, ok := props.Get(mapi.MakeTag(ids[0], mapi.PtMvUnicode)); ok {
		if cats, ok := v.([]string); ok {
			return cats
		}
	}
	return nil
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
