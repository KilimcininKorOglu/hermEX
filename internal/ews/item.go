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
		s.soapFault(w, "ErrorInvalidRequest", "FindItem: invalid request", err)
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()

	var msgs []findItemResponseMessage
	for _, tgt := range resolveTargets(req.ParentFolderIDs) {
		msgs = append(msgs, findItemForTarget(cache, sess, tgt))
	}
	writeResponse(w, findItemResponse{Messages: msgs})
}

// findItemForTarget lists one requested folder.
func findItemForTarget(cache *storeCache, sess *session, tgt folderTarget) findItemResponseMessage {
	// A recoverable (Recoverable Items dumpster) target is intentionally ok=false so
	// every other handler still reports ErrorFolderNotFound; FindItem serves it.
	if !tgt.ok && !tgt.recoverable {
		return findItemError(tgt.code)
	}
	if tgt.public {
		// The public folders root is a container holding no items of its own; its
		// public child folders carry the items and are addressed by their own ids.
		return findItemFound(&findItemRoot{IncludesLastItemInRange: true})
	}
	if tgt.recoverable {
		return recoverableItemsFor(cache, sess, tgt)
	}
	st, _, isOwn, code := cache.open(sess, tgt.mailbox)
	if code == codePublicAbsent {
		code = "ErrorFolderNotFound" // a public folder whose domain store is gone
	}
	if code != "" {
		return findItemError(code)
	}
	// Listing another mailbox's folder requires only folder visibility; reading an
	// item's content (GetItem) is separately gated on read access. This two-tier
	// model matches the EWS enforcement contract.
	if !isOwn {
		if code := folderVisibleAccess(st, tgt.fid, sess.user); code != "" {
			return findItemError(code)
		}
	}
	return folderItemsFound(st, tgt.fid, delegatedMailbox(tgt, isOwn))
}

// recoverableItemsFor lists the Recoverable Items dumpster, which aggregates
// soft-deleted items mailbox-wide. It is served only for the caller's own mailbox
// (no per-folder ACL applies to an aggregate), and each item keeps its original
// parent folder in its id.
func recoverableItemsFor(cache *storeCache, sess *session, tgt folderTarget) findItemResponseMessage {
	st, _, isOwn, code := cache.open(sess, tgt.mailbox)
	if code != "" {
		return findItemError(code)
	}
	if !isOwn {
		return findItemError("ErrorAccessDenied")
	}
	items, err := st.ListAllSoftDeleted()
	if err != nil {
		return findItemError("ErrorInternalServerError")
	}
	elems := make([]oxews.Message, 0, len(items))
	for _, it := range items {
		elems = append(elems, itemSummary(st, it.FolderID, it.Info, ""))
	}
	return findItemFound(&findItemRoot{
		TotalItemsInView:        len(elems),
		IncludesLastItemInRange: true,
		Items:                   itemsWrap{Messages: elems},
	})
}

// folderItemsFound lists one folder's items in the shape its class calls for.
// Tasks and notes live in the object store (versioned by change number), not the
// IMAP index, so they are listed as folder objects rather than messages.
func folderItemsFound(st *objectstore.Store, fid int64, idMailbox string) findItemResponseMessage {
	switch fid {
	case int64(mapi.PrivateFIDTasks):
		objs, _ := st.ListFolderObjects(fid)
		tasks := make([]oxews.Task, 0, len(objs))
		for _, o := range objs {
			tasks = append(tasks, taskSummary(st, fid, o.ID, idMailbox))
		}
		return findItemFound(&findItemRoot{
			TotalItemsInView:        len(tasks),
			IncludesLastItemInRange: true,
			Items:                   itemsWrap{Tasks: tasks},
		})
	case int64(mapi.PrivateFIDNotes):
		objs, _ := st.ListFolderObjects(fid)
		notes := make([]oxews.Item, 0, len(objs))
		for _, o := range objs {
			notes = append(notes, noteSummary(st, fid, o.ID, idMailbox))
		}
		return findItemFound(&findItemRoot{
			TotalItemsInView:        len(notes),
			IncludesLastItemInRange: true,
			Items:                   itemsWrap{BaseItems: notes},
		})
	}
	items, err := st.ListMessages(fid)
	if err != nil {
		return findItemError("ErrorItemNotFound")
	}
	elems := make([]oxews.Message, 0, len(items))
	for _, info := range items {
		elems = append(elems, itemSummary(st, fid, info, idMailbox))
	}
	return findItemFound(&findItemRoot{
		TotalItemsInView:        len(elems),
		IncludesLastItemInRange: true,
		Items:                   itemsWrap{Messages: elems},
	})
}

// folderVisibleAccess reports the response code refusing a caller who cannot see
// a folder they do not own; an empty code means the folder is visible to them.
func folderVisibleAccess(st *objectstore.Store, fid int64, user string) string {
	rights, err := st.ResolvePermission(fid, user)
	if err != nil {
		return "ErrorInternalServerError"
	}
	if rights&mapi.FrightsVisible == 0 {
		return "ErrorAccessDenied"
	}
	return ""
}

// delegatedMailbox returns the mailbox to stamp into the item ids: empty for the
// caller's own mailbox, the target's for a delegated one, so the client reopens
// the right mailbox on a follow-up.
func delegatedMailbox(tgt folderTarget, isOwn bool) string {
	if isOwn {
		return ""
	}
	return tgt.mailbox
}

// findItemError builds a FindItem error response message.
func findItemError(code string) findItemResponseMessage {
	return findItemResponseMessage{ResponseClass: "Error", ResponseCode: code}
}

// findItemFound builds a FindItem success response message over one root folder.
func findItemFound(root *findItemRoot) findItemResponseMessage {
	return findItemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError", RootFolder: root}
}

// handleGetItem answers GetItem: each requested item is opened, its body read
// from the message's RFC822 form (the store keeps no HTML body property), and
// rendered as a full <t:Message>.
func (s *Server) handleGetItem(w http.ResponseWriter, inner []byte, sess *session) {
	var req getItemRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "GetItem: invalid request", err)
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()

	var msgs []itemResponseMessage
	for _, ref := range req.ItemIDs.Items {
		msgs = append(msgs, getOneItem(cache, sess, ref.ID))
	}
	writeResponse(w, getItemResponse{Messages: msgs})
}

// getOneItem renders one requested item in the shape its class calls for.
func getOneItem(cache *storeCache, sess *session, itemID string) itemResponseMessage {
	id, err := oxews.DecodeItemID(itemID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	// The id self-encodes its mailbox: an own-mailbox id (empty) opens the caller's
	// store, a delegated id opens the target and is gated on the caller's read access.
	st, _, isOwn, code := cache.open(sess, id.Mailbox)
	if code == codePublicAbsent {
		code = "ErrorItemNotFound" // a public item whose domain store is gone
	}
	if code != "" {
		return itemError(code)
	}
	if !isOwn {
		if code := checkItemAccess(st, id, sess.user, mapi.FrightsReadAny); code != "" {
			return itemError(code)
		}
	}
	msg, err := st.OpenMessage(id.MessageID)
	if err != nil {
		return itemError("ErrorItemNotFound")
	}
	hasAttach, _ := st.HasAttachments(id.MessageID)
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	changeKey := oxews.ChangeKey(uint64(id.MessageID))
	switch itemClass(msg.Props) {
	case oxtask.MessageClass:
		// A task is rendered as <t:Task> from its shared properties, not the mail
		// MIME path (a task has no RFC822 form).
		tk, _ := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
		elem := oxews.BuildTask(tk, oxews.ItemMeta{ItemID: itemID, ChangeKey: changeKey, HasAttachments: hasAttach})
		return itemFound(&itemsWrap{Tasks: []oxews.Task{elem}})
	case oxews.NoteClass:
		// A sticky note is rendered as a base <t:Item> (EWS has no Note type) from
		// its shared properties.
		elem := buildNoteItem(st, msg.Props, itemID, changeKey)
		return itemFound(&itemsWrap{BaseItems: []oxews.Item{elem}})
	}
	info, _ := st.MessageByUID(id.FolderID, id.UID)
	body, bodyType := "", "Text"
	if raw, err := st.GetMessageRaw(id.FolderID, id.UID); err == nil {
		body, bodyType = bodyFromRaw(raw)
	}
	elem := oxews.BuildItem(msg, oxews.ItemMeta{
		ItemID:         itemID,
		FolderID:       id.FolderID,
		MessageID:      id.MessageID,
		Mailbox:        id.Mailbox,
		ChangeKey:      changeKey,
		IsRead:         info.Flags&objectstore.FlagSeen != 0,
		HasAttachments: hasAttach,
		Received:       info.InternalDate,
		Size:           int(info.Size),
		Body:           body,
		BodyType:       bodyType,
	})
	return itemFound(&itemsWrap{Messages: []oxews.Message{elem}})
}

// itemFound builds a GetItem success response message over one rendered item.
func itemFound(items *itemsWrap) itemResponseMessage {
	return itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError", Items: items}
}

// handleGetAttachment answers GetAttachment: each attachment id is resolved to
// its message and index, and the full FileAttachment (with base64 content) is
// returned.
func (s *Server) handleGetAttachment(w http.ResponseWriter, inner []byte, sess *session) {
	var req getAttachmentRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "GetAttachment: invalid request", err)
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()

	var msgs []getAttachmentResponseMessage
	for _, ref := range req.AttachmentIDs.IDs {
		msgs = append(msgs, getOneAttachment(cache, sess, ref.ID))
	}
	writeResponse(w, getAttachmentResponse{Messages: msgs})
}

// getOneAttachment resolves one attachment id to its content.
func getOneAttachment(cache *storeCache, sess *session, attachmentID string) getAttachmentResponseMessage {
	folderID, mid, idx, mailbox, err := oxews.DecodeAttachmentID(attachmentID)
	if err != nil {
		return getAttachmentError("ErrorInvalidRequest")
	}
	// The id self-encodes its mailbox and parent folder; a delegated attachment is
	// gated on read access to that folder (reference: GetAttachment checks
	// frightsReadAny on the attachment's parent folder).
	st, _, isOwn, code := cache.open(sess, mailbox)
	if code == codePublicAbsent {
		code = "ErrorItemNotFound" // a public attachment whose domain store is gone
	}
	if code != "" {
		return getAttachmentError(code)
	}
	if !isOwn {
		id := oxews.ItemID{FolderID: folderID, MessageID: mid, Mailbox: mailbox}
		if code := checkItemAccess(st, id, sess.user, mapi.FrightsReadAny); code != "" {
			return getAttachmentError(code)
		}
	}
	msg, err := st.OpenMessage(mid)
	if err != nil || idx < 0 || idx >= len(msg.Attachments) {
		return getAttachmentError("ErrorItemNotFound")
	}
	att := msg.Attachments[idx]
	if oxews.IsEmbeddedAttachment(att) {
		// An embedded message is returned as an ItemAttachment carrying the nested
		// message item, not a file blob.
		ia := oxews.BuildItemAttachmentContent(folderID, mid, idx, att, mailbox)
		return getAttachmentFound(&attachmentsWrap{Items: []oxews.ItemAttachment{ia}})
	}
	fa := oxews.BuildAttachmentContent(folderID, mid, idx, att, mailbox)
	return getAttachmentFound(&attachmentsWrap{Files: []oxews.FileAttachment{fa}})
}

// getAttachmentError builds a GetAttachment error response message.
func getAttachmentError(code string) getAttachmentResponseMessage {
	return getAttachmentResponseMessage{ResponseClass: "Error", ResponseCode: code}
}

// getAttachmentFound builds a GetAttachment success response message.
func getAttachmentFound(atts *attachmentsWrap) getAttachmentResponseMessage {
	return getAttachmentResponseMessage{ResponseClass: "Success", ResponseCode: "NoError", Attachments: atts}
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
		ItemID: oxews.EncodeItemID(oxews.ItemID{FolderID: folderID, MessageID: info.ID, UID: info.UID, Mailbox: mailbox}),
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
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
		ItemID: oxews.EncodeItemID(oxews.ItemID{FolderID: folderID, MessageID: objectID, Mailbox: mailbox}),
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
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
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
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
