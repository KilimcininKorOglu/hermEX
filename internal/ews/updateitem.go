package ews

import (
	"encoding/xml"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// --- UpdateItem ---

type updateItemRequest struct {
	// MessageDisposition decides what happens after the update: SaveOnly (the
	// default) leaves the message where it is, SendOnly and SendAndSaveCopy
	// transmit it. A client that composes by creating a draft and then updating
	// it sends through this attribute and never calls SendItem at all.
	MessageDisposition string `xml:"MessageDisposition,attr"`
	// SuppressReadReceipts stops the read receipt that marking a message read
	// otherwise sends ([MS-OXWSCORE] 3.1.4.9.3.1).
	SuppressReadReceipts bool `xml:"SuppressReadReceipts,attr"`
	// SendMeetingInvitationsOrCancellations decides whether an edit of a meeting
	// is sent to its attendees; a request changing a calendar item must carry it.
	SendMeetingInvitationsOrCancellations string `xml:"SendMeetingInvitationsOrCancellations,attr"`
	ItemChanges                           struct {
		Changes []itemChangeReq `xml:"ItemChange"`
	} `xml:"ItemChanges"`
}

type itemChangeReq struct {
	ItemID refID `xml:"ItemId"`
	// Occurrence names the item as one occurrence of a series by its master and
	// index, in place of an ItemId.
	Occurrence *struct {
		MasterID string `xml:"RecurringMasterId,attr"`
		Index    int    `xml:"InstanceIndex,attr"`
	} `xml:"OccurrenceItemId"`
	Updates struct {
		SetFields    []setItemField    `xml:"SetItemField"`
		DeleteFields []deleteItemField `xml:"DeleteItemField"`
	} `xml:"Updates"`
}

type setItemField struct {
	FieldURI struct {
		URI string `xml:"FieldURI,attr"`
	} `xml:"FieldURI"`
	// Extended names an extended property to set instead of a FieldURI; its value
	// is the matching <t:ExtendedProperty> of the item element.
	Extended *oxews.ExtendedFieldURI `xml:"ExtendedFieldURI"`
	Message  updateMessageFields     `xml:"Message"`
	// CalendarItem carries the value of a field set on a calendar item.
	CalendarItem createCalendarItem `xml:"CalendarItem"`
	// Task carries the value of a field set on a task.
	Task createTask `xml:"Task"`
	// Item carries the value of a field set on a base item: a sticky note, or an
	// extended property on any item.
	Item noteItem `xml:"Item"`
}

// deleteItemField is a <t:DeleteItemField>. An extended property is removed this
// way on any item; a field named by FieldURI only on a task, whose optional fields
// a client clears so.
type deleteItemField struct {
	FieldURI struct {
		URI string `xml:"FieldURI,attr"`
	} `xml:"FieldURI"`
	Extended *oxews.ExtendedFieldURI `xml:"ExtendedFieldURI"`
}

// updateMessageFields is the <t:Message> a SetItemField carries. Every update
// repeats the whole element, so only the field the FieldURI names is read.
type updateMessageFields struct {
	Subject string `xml:"Subject"`
	Body    struct {
		Type    string `xml:"BodyType,attr"`
		Content string `xml:",chardata"`
	} `xml:"Body"`
	ToRecipients  mailboxList              `xml:"ToRecipients"`
	CcRecipients  mailboxList              `xml:"CcRecipients"`
	BccRecipients mailboxList              `xml:"BccRecipients"`
	IsRead        string                   `xml:"IsRead"`
	Extended      []oxews.ExtendedProperty `xml:"ExtendedProperty"`
}

// The item fields UpdateItem writes. A field outside this set is refused rather
// than accepted and dropped, because a client that is told its edit succeeded
// has no way to learn that the message still holds the old value.
const (
	fieldSubject = "item:Subject"
	fieldBody    = "item:Body"
	fieldTo      = "message:ToRecipients"
	fieldCc      = "message:CcRecipients"
	fieldBcc     = "message:BccRecipients"
	fieldIsRead  = "message:IsRead"
)

// rewritesContent reports whether a field changes what the message says, as
// opposed to the read flag, which is index state and needs no rewrite.
func rewritesContent(uri string) bool {
	switch uri {
	case fieldSubject, fieldBody, fieldTo, fieldCc, fieldBcc:
		return true
	}
	return false
}

type updateItemResponse struct {
	XMLName  xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateItemResponse"`
	Messages []itemResponseMessage `xml:"ResponseMessages>UpdateItemResponseMessage"`
}

// handleUpdateItem answers UpdateItem ([MS-OXWSCORE] 3.1.4.9). It writes the
// subject, the body, the To/Cc/Bcc recipients and the read flag, which is what a
// client editing a draft sends. A field it does not write is REFUSED, never
// accepted and dropped: a client told its edit succeeded has no way to learn
// that the message still holds the old value.
func (s *Server) handleUpdateItem(w http.ResponseWriter, inner []byte, sess *session) {
	var req updateItemRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "UpdateItem: invalid request", err)
		return
	}
	disp := req.MessageDisposition
	if disp == "" {
		disp = "SaveOnly"
	}
	if disp != "SaveOnly" && disp != "SendOnly" && disp != "SendAndSaveCopy" {
		writeResponse(w, updateItemResponse{Messages: []itemResponseMessage{itemError("ErrorInvalidRequest")}})
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()

	var msgs []itemResponseMessage
	for _, ch := range req.ItemChanges.Changes {
		if ch.Occurrence != nil {
			token, code := occurrenceToken(cache, sess, itemRef{MasterID: ch.Occurrence.MasterID, Index: ch.Occurrence.Index})
			if code != "" {
				msgs = append(msgs, itemError(code))
				continue
			}
			ch.ItemID.ID = token
		}
		msgs = append(msgs, s.updateOne(cache, sess, ch, updateOptions{disp: disp, suppress: req.SuppressReadReceipts, send: req.SendMeetingInvitationsOrCancellations}))
	}
	writeResponse(w, updateItemResponse{Messages: msgs})
}

// updateOptions are the request attributes an ItemChange is applied under: disp is
// the MessageDisposition, which decides whether the updated message is also
// transmitted; suppress stops the read receipt marking it read would send; send
// decides whether an edited meeting goes to its attendees.
type updateOptions struct {
	disp, send string
	suppress   bool
}

// updateOne applies one ItemChange and returns its response message. An item of
// the object store alone (a calendar item or occurrence) has no IMAP uid and is
// edited by its object id.
func (s *Server) updateOne(cache *storeCache, sess *session, ch itemChangeReq, o updateOptions) itemResponseMessage {
	id, err := oxews.DecodeAnyItemID(ch.ItemID.ID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	// The id self-encodes its mailbox; a delegated item is gated on edit access.
	st, code := cache.openForItem(sess, id, mapi.FrightsEditAny)
	if code != "" {
		return itemError(code)
	}
	if id.UID == 0 && isTaskItem(st, id.MessageID) {
		return updateTask(st, id, ch)
	}
	if id.UID == 0 && isNoteItem(st, id.MessageID) {
		return updateNote(st, id, ch)
	}
	if id.UID == 0 {
		return s.updateCalendarItem(st, id, ch, o.send, sess.user)
	}
	return s.updateMessage(cache, sess, st, id, ch, o)
}

// updateMessage applies one ItemChange to an indexed message.
func (s *Server) updateMessage(cache *storeCache, sess *session, st *objectstore.Store, id oxews.ItemID, ch itemChangeReq, o updateOptions) itemResponseMessage {
	disp, suppress := o.disp, o.suppress
	ext, code := checkedChange(st, ch)
	if code != "" {
		return itemError(code)
	}
	// The content rewrite runs first and yields a new uid, so the read flag is
	// then set on the message that survives.
	newID := ch.ItemID.ID
	if hasContentUpdate(ch.Updates.SetFields) || !ext.empty() {
		info, err := rewriteItem(st, id, ch.Updates.SetFields, ext)
		if err != nil {
			return itemError("ErrorItemNotFound")
		}
		id.MessageID, id.UID = info.ID, info.UID
		newID = oxews.EncodeItemID(id)
	}
	if err := s.updateReadFlag(st, sess, id, ch.Updates.SetFields, suppress); err != nil {
		return itemError("ErrorItemNotFound")
	}
	// The send runs on the updated message, so the recipients and body the same
	// request just wrote are the ones that go out. sendOne consumes the draft, so
	// the response carries no item id: the message the client asked about is gone
	// from where it was.
	if sends(disp) {
		rm := s.sendOne(cache, sess, newID, disp == "SendAndSaveCopy", int64(mapi.PrivateFIDSentItems))
		if rm.Items == nil {
			rm.Items = &itemsWrap{}
		}
		return rm
	}
	return itemResponseMessage{
		ResponseClass: "Success", ResponseCode: "NoError",
		Items: &itemsWrap{Messages: []oxews.Message{{ItemID: oxews.ItemIDElem{ID: newID, ChangeKey: changeKey(st, id.MessageID)}}}},
	}
}

// checkedChange refuses an ItemChange naming a field the handler does not write
// and reads its extended-property updates.
func checkedChange(st *objectstore.Store, ch itemChangeReq) (extUpdate, string) {
	if !everyFieldIsWritten(ch.Updates.SetFields) {
		return extUpdate{}, "ErrorInvalidPropertySet"
	}
	return extendedUpdate(st, ch)
}

// everyFieldIsWritten reports whether the handler writes every field named.
func everyFieldIsWritten(fields []setItemField) bool {
	for _, sf := range fields {
		if sf.Extended == nil && !rewritesContent(sf.FieldURI.URI) && sf.FieldURI.URI != fieldIsRead {
			return false
		}
	}
	return true
}

// sends reports whether a disposition transmits the message.
func sends(disp string) bool {
	return disp == "SendOnly" || disp == "SendAndSaveCopy"
}

// hasContentUpdate reports whether any field changes what the message says.
func hasContentUpdate(fields []setItemField) bool {
	for _, sf := range fields {
		if rewritesContent(sf.FieldURI.URI) {
			return true
		}
	}
	return false
}

// updateReadFlag writes the read flag when the request names it and sends the read
// receipt a message taken from unread to read asks for, unless suppress is set.
func (s *Server) updateReadFlag(st *objectstore.Store, sess *session, id oxews.ItemID, fields []setItemField, suppress bool) error {
	becameRead, err := applyReadFlag(st, id, fields)
	if err != nil {
		return err
	}
	if becameRead && !suppress {
		s.sendReadReceipt(st, sess, id.Mailbox, id.MessageID)
	}
	return nil
}

// applyReadFlag writes the read flag when the request names it. becameRead reports
// whether the message went from unread to read, which is the only change that owes
// a read receipt.
func applyReadFlag(st *objectstore.Store, id oxews.ItemID, fields []setItemField) (becameRead bool, err error) {
	for _, sf := range fields {
		if sf.FieldURI.URI != fieldIsRead {
			continue
		}
		flags, err := st.MessageFlags(id.FolderID, id.UID)
		if err != nil {
			return false, err
		}
		wasRead := flags&objectstore.FlagSeen != 0
		read := strings.EqualFold(strings.TrimSpace(sf.Message.IsRead), "true")
		if read {
			flags |= objectstore.FlagSeen
		} else {
			flags &^= objectstore.FlagSeen
		}
		if err := st.SetMessageFlags(id.FolderID, id.UID, flags); err != nil {
			return false, err
		}
		becameRead = read && !wasRead
	}
	return becameRead, nil
}

// rewriteItem applies the content updates to the stored message in place and
// returns its index row. Only the properties the updates changed are written or
// removed, so every other property and every attachment stays, and the message
// keeps its id. The recipients are rewritten when an update names a recipient
// class. The message is then indexed again under a new uid, because an IMAP
// client treats a uid's content as immutable and would otherwise keep serving
// the old subject and body; the new uid reaches the client in the response.
func rewriteItem(st *objectstore.Store, id oxews.ItemID, fields []setItemField, ext extUpdate) (objectstore.MessageInfo, error) {
	msg, err := st.OpenMessage(id.MessageID)
	if err != nil {
		return objectstore.MessageInfo{}, err
	}
	stored := slices.Clone(msg.Props)
	for _, sf := range fields {
		applyField(msg, sf)
	}
	ext.apply(&msg.Props)
	oxcmail.EnsureMessageID(&msg.Props)
	set, removed := changedProps(stored, msg.Props)
	if err := st.ModifyMessageProperties(id.MessageID, set, removed...); err != nil {
		return objectstore.MessageInfo{}, err
	}
	if namesRecipients(fields) {
		if err := st.ReplaceRecipients(id.MessageID, msg.Recipients); err != nil {
			return objectstore.MessageInfo{}, err
		}
	}
	return st.ReindexMessage(id.MessageID)
}

// changedProps compares a message's properties before and after the updates:
// set holds each property that is new or holds a new value, removed each tag
// the updates dropped.
func changedProps(before, after mapi.PropertyValues) (set mapi.PropertyValues, removed []mapi.PropTag) {
	for _, pv := range after {
		if old, ok := before.Get(pv.Tag); !ok || !reflect.DeepEqual(old, pv.Value) {
			set = append(set, pv)
		}
	}
	for _, pv := range before {
		if !after.Has(pv.Tag) {
			removed = append(removed, pv.Tag)
		}
	}
	return set, removed
}

// namesRecipients reports whether an update replaces a recipient class.
func namesRecipients(fields []setItemField) bool {
	for _, sf := range fields {
		switch sf.FieldURI.URI {
		case fieldTo, fieldCc, fieldBcc:
			return true
		}
	}
	return false
}

// applyField writes one update onto the stored message.
func applyField(msg *oxcmail.Message, sf setItemField) {
	switch sf.FieldURI.URI {
	case fieldSubject:
		oxcmail.SetSubject(&msg.Props, sf.Message.Subject)
	case fieldBody:
		setBody(msg, sf.Message.Body.Type, sf.Message.Body.Content)
	case fieldTo:
		setRecipients(msg, mapi.RecipTo, sf.Message.ToRecipients)
	case fieldCc:
		setRecipients(msg, mapi.RecipCc, sf.Message.CcRecipients)
	case fieldBcc:
		setRecipients(msg, mapi.RecipBcc, sf.Message.BccRecipients)
	}
}

// setBody replaces the body in the requested format and removes the other one,
// because a message carrying both would export the stale half.
func setBody(msg *oxcmail.Message, bodyType, content string) {
	if strings.EqualFold(bodyType, "HTML") {
		msg.Props.Set(mapi.PrHTML, []byte(oxews.ToCRLF(content)))
		msg.Props.Remove(mapi.PrBody)
		return
	}
	msg.Props.Set(mapi.PrBody, oxews.ToCRLF(content))
	msg.Props.Remove(mapi.PrHTML)
}

// setRecipients replaces every recipient of one class, leaving the other classes
// alone: a request that names only ToRecipients must not drop the Cc list.
func setRecipients(msg *oxcmail.Message, rcptType int32, list mailboxList) {
	kept := make([]mapi.PropertyValues, 0, len(msg.Recipients))
	for _, bag := range msg.Recipients {
		if rt, _ := bag.Get(mapi.PrRecipientType); rt != rcptType {
			kept = append(kept, bag)
		}
	}
	msg.Recipients = append(kept, oxews.RecipientBags(toMailboxes(list), rcptType)...)
}

// --- DeleteItem ---

type deleteItemRequest struct {
	DeleteType string `xml:"DeleteType,attr"`
	// SendMeetingCancellations decides whether deleting a meeting tells its
	// attendees; a request deleting a calendar item must carry it.
	SendMeetingCancellations string   `xml:"SendMeetingCancellations,attr"`
	ItemIDs                  itemRefs `xml:"ItemIds"`
}

type deleteItemResponse struct {
	XMLName  xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItemResponse"`
	Messages []itemResponseMessage `xml:"ResponseMessages>DeleteItemResponseMessage"`
}

// handleDeleteItem answers DeleteItem: HardDelete and SoftDelete send the message
// to the Recoverable Items dumpster (soft delete, recoverable until retention);
// MoveToDeletedItems moves it to Deleted Items.
func (s *Server) handleDeleteItem(w http.ResponseWriter, inner []byte, sess *session) {
	var req deleteItemRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "DeleteItem: invalid request", err)
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()

	var msgs []itemResponseMessage
	for _, ref := range req.ItemIDs.Items {
		msgs = append(msgs, s.deleteOne(cache, sess, ref, req))
	}
	writeResponse(w, deleteItemResponse{Messages: msgs})
}

// deleteOne deletes one item of a DeleteItem. An item of the object store alone (a
// calendar item or occurrence, a task, a note) has no IMAP uid and is deleted by
// its object id.
func (s *Server) deleteOne(cache *storeCache, sess *session, ref itemRef, req deleteItemRequest) itemResponseMessage {
	token, code := resolveItemRef(cache, sess, ref)
	if code != "" {
		return itemError(code)
	}
	id, err := oxews.DecodeAnyItemID(token)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	// The id self-encodes its mailbox; a delegated item is gated on delete access.
	st, code := cache.openForItem(sess, id, mapi.FrightsDeleteAny)
	if code != "" {
		return itemError(code)
	}
	if id.UID == 0 {
		code = s.deleteObjectItem(objectDelete{st: st, id: id, deleteType: req.DeleteType, cancellations: req.SendMeetingCancellations, caller: sess.user})
	} else if err := deleteMessage(st, id, req.DeleteType); err != nil {
		code = "ErrorItemNotFound"
	}
	if code != "" {
		return itemError(code)
	}
	return itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"}
}

// deleteMessage deletes one indexed message: HardDelete and SoftDelete into the
// Recoverable Items dumpster, anything else into Deleted Items.
func deleteMessage(st *objectstore.Store, id oxews.ItemID, deleteType string) error {
	if deleteType == "HardDelete" || deleteType == "SoftDelete" {
		return st.SoftDeleteMessage(id.FolderID, id.UID)
	}
	_, err := moveMessage(st, id.FolderID, id.UID, int64(mapi.PrivateFIDDeletedItems))
	return err
}

// --- MoveItem / CopyItem ---

type moveCopyItemRequest struct {
	ToFolderID folderRefs `xml:"ToFolderId"`
	ItemIDs    itemRefs   `xml:"ItemIds"`
}

type moveItemResponse struct {
	XMLName  xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MoveItemResponse"`
	Messages []itemResponseMessage `xml:"ResponseMessages>MoveItemResponseMessage"`
}

type copyItemResponse struct {
	XMLName  xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CopyItemResponse"`
	Messages []itemResponseMessage `xml:"ResponseMessages>CopyItemResponseMessage"`
}

// handleMoveItem answers MoveItem: each item is copied to the target folder and
// removed from its source (fresh uid), returning the new ItemId.
func (s *Server) handleMoveItem(w http.ResponseWriter, inner []byte, sess *session) {
	s.moveOrCopy(w, inner, sess, true)
}

// handleCopyItem answers CopyItem: each item is copied to the target folder,
// leaving the source in place, returning the new ItemId.
func (s *Server) handleCopyItem(w http.ResponseWriter, inner []byte, sess *session) {
	s.moveOrCopy(w, inner, sess, false)
}

func (s *Server) moveOrCopy(w http.ResponseWriter, inner []byte, sess *session, remove bool) {
	var req moveCopyItemRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "Move/CopyItem: invalid request", err)
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()
	dest, code := openMoveCopyDest(cache, sess, req.ToFolderID)
	if code != "" {
		writeMoveCopy(w, remove, []itemResponseMessage{itemError(code)})
		return
	}

	var msgs []itemResponseMessage
	for _, ref := range req.ItemIDs.Items {
		token, code := resolveItemRef(cache, sess, ref)
		if code != "" {
			msgs = append(msgs, itemError(code))
			continue
		}
		msgs = append(msgs, moveCopyOne(cache, sess, dest, token, remove))
	}
	writeMoveCopy(w, remove, msgs)
}

// moveCopyDest is the gated destination one Move/CopyItem writes into.
type moveCopyDest struct {
	st      *objectstore.Store
	fid     int64
	mailbox string // stamped into the new item ids, empty for the caller's own mailbox
}

// openMoveCopyDest resolves and gates the destination folder once: a folder in a
// mailbox the caller does not own requires create access.
func openMoveCopyDest(cache *storeCache, sess *session, refs folderRefs) (moveCopyDest, string) {
	targets := resolveTargets(refs)
	if len(targets) == 0 {
		return moveCopyDest{}, "ErrorInvalidRequest"
	}
	if !targets[0].ok {
		return moveCopyDest{}, targets[0].code
	}
	st, _, isOwn, code := cache.open(sess, targets[0].mailbox)
	if code != "" {
		return moveCopyDest{}, code
	}
	if !isOwn {
		if code := folderCreateAccess(st, targets[0].fid, sess.user); code != "" {
			return moveCopyDest{}, code
		}
	}
	return moveCopyDest{st: st, fid: targets[0].fid, mailbox: delegatedMailbox(targets[0], isOwn)}, ""
}

// folderCreateAccess reports the response code refusing a caller who cannot
// create items in a folder they do not own; an empty code means they may.
func folderCreateAccess(st *objectstore.Store, fid int64, user string) string {
	rights, err := st.ResolvePermission(fid, user)
	if err != nil {
		return "ErrorInternalServerError"
	}
	if rights&mapi.FrightsCreate == 0 {
		return "ErrorAccessDenied"
	}
	return ""
}

// moveCopyOne moves or copies one item into the gated destination.
func moveCopyOne(cache *storeCache, sess *session, dest moveCopyDest, itemID string, remove bool) itemResponseMessage {
	id, err := oxews.DecodeAnyItemID(itemID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	if id.Instance != 0 {
		return itemError("ErrorCalendarCannotMoveOrCopyOccurrence")
	}
	if code := checkMoveCopySource(cache, sess, dest, id, remove); code != "" {
		return itemError(code)
	}
	if id.UID == 0 {
		return moveCopyObject(dest, id, remove)
	}
	info, err := applyMoveCopy(dest, id, remove)
	if err != nil {
		return itemError("ErrorItemNotFound")
	}
	newID := oxews.EncodeItemID(oxews.ItemID{FolderID: dest.fid, MessageID: info.ID, UID: info.UID, Mailbox: dest.mailbox})
	return itemResponseMessage{
		ResponseClass: "Success", ResponseCode: "NoError",
		Items: &itemsWrap{Messages: []oxews.Message{{ItemID: oxews.ItemIDElem{ID: newID, ChangeKey: changeKey(dest.st, info.ID)}}}},
	}
}

// checkMoveCopySource opens and gates the source of one item; an empty code means
// the operation may proceed.
func checkMoveCopySource(cache *storeCache, sess *session, dest moveCopyDest, id oxews.ItemID, remove bool) string {
	srcSt, _, srcOwn, code := cache.open(sess, id.Mailbox)
	if code != "" {
		return code
	}
	// The copy runs within a single store; moving an item across mailboxes is not
	// supported (the source and destination must be the same mailbox).
	if srcSt != dest.st {
		return "ErrorAccessDenied"
	}
	if srcOwn {
		return ""
	}
	// A non-own source is gated on delete (move) or read (copy) of the source folder.
	need := mapi.FrightsReadAny
	if remove {
		need = mapi.FrightsDeleteAny
	}
	// checkItemAccess also binds the message to the folder that was authorized.
	// The recovery below addresses a soft-deleted message by id alone, so without
	// that binding a delegate could pair a folder they hold rights on with a
	// message deleted from a folder they cannot reach, and restore it into a
	// folder they can read.
	return checkItemAccess(srcSt, id, sess.user, need)
}

// applyMoveCopy performs the move or copy. A soft-deleted source item (recovered
// from the Recoverable Items dumpster) has no live uid, so a move on it is a
// recovery into the chosen target folder; a live item falls through to a normal
// move.
func applyMoveCopy(dest moveCopyDest, id oxews.ItemID, remove bool) (objectstore.MessageInfo, error) {
	if !remove {
		return copyMessage(dest.st, id.FolderID, id.UID, dest.fid)
	}
	info, err := dest.st.RecoverMessageTo(id.MessageID, dest.fid)
	if errors.Is(err, objectstore.ErrNotFound) {
		return moveMessage(dest.st, id.FolderID, id.UID, dest.fid)
	}
	return info, err
}

func writeMoveCopy(w http.ResponseWriter, moved bool, msgs []itemResponseMessage) {
	if moved {
		writeResponse(w, moveItemResponse{Messages: msgs})
	} else {
		writeResponse(w, copyItemResponse{Messages: msgs})
	}
}

// copyMessage copies a message into the target folder, preserving its flags and
// date, and returns the new message info.
func copyMessage(st *objectstore.Store, fromFID int64, uid uint32, toFID int64) (objectstore.MessageInfo, error) {
	raw, err := st.GetMessageRaw(fromFID, uid)
	if err != nil {
		return objectstore.MessageInfo{}, err
	}
	flags := int64(0)
	date := time.Now()
	if info, err := st.MessageByUID(fromFID, uid); err == nil {
		flags = info.Flags
		date = info.InternalDate
	}
	return st.AppendMessage(toFID, raw, date, flags)
}

// moveMessage copies a message into the target folder then removes the source.
func moveMessage(st *objectstore.Store, fromFID int64, uid uint32, toFID int64) (objectstore.MessageInfo, error) {
	info, err := copyMessage(st, fromFID, uid, toFID)
	if err != nil {
		return objectstore.MessageInfo{}, err
	}
	if err := st.DeleteMessage(fromFID, uid); err != nil {
		return objectstore.MessageInfo{}, err
	}
	return info, nil
}
