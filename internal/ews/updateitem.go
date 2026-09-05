package ews

import (
	"encoding/xml"
	"errors"
	"net/http"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// --- UpdateItem ---

type updateItemRequest struct {
	ItemChanges struct {
		Changes []itemChangeReq `xml:"ItemChange"`
	} `xml:"ItemChanges"`
}

type itemChangeReq struct {
	ItemID  refID `xml:"ItemId"`
	Updates struct {
		SetFields []setItemField `xml:"SetItemField"`
	} `xml:"Updates"`
}

type setItemField struct {
	FieldURI struct {
		URI string `xml:"FieldURI,attr"`
	} `xml:"FieldURI"`
	Message updateMessageFields `xml:"Message"`
}

// updateMessageFields is the <t:Message> a SetItemField carries. Every update
// repeats the whole element, so only the field the FieldURI names is read.
type updateMessageFields struct {
	Subject string `xml:"Subject"`
	Body    struct {
		Type    string `xml:"BodyType,attr"`
		Content string `xml:",chardata"`
	} `xml:"Body"`
	ToRecipients  mailboxList `xml:"ToRecipients"`
	CcRecipients  mailboxList `xml:"CcRecipients"`
	BccRecipients mailboxList `xml:"BccRecipients"`
	IsRead        string      `xml:"IsRead"`
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
	cache := s.newStoreCache()
	defer cache.closeAll()

	var msgs []itemResponseMessage
	for _, ch := range req.ItemChanges.Changes {
		msgs = append(msgs, s.updateOne(cache, sess, ch))
	}
	writeResponse(w, updateItemResponse{Messages: msgs})
}

// updateOne applies one ItemChange and returns its response message.
func (s *Server) updateOne(cache *storeCache, sess *session, ch itemChangeReq) itemResponseMessage {
	id, err := oxews.DecodeItemID(ch.ItemID.ID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	// The id self-encodes its mailbox; a delegated item is gated on edit access.
	st, code := cache.openForItem(sess, id, mapi.FrightsEditAny)
	if code != "" {
		return itemError(code)
	}
	for _, sf := range ch.Updates.SetFields {
		if !rewritesContent(sf.FieldURI.URI) && sf.FieldURI.URI != fieldIsRead {
			return itemError("ErrorInvalidPropertySet")
		}
	}
	// The content rewrite runs first and yields a new uid, so the read flag is
	// then set on the message that survives.
	newID := ch.ItemID.ID
	if hasContentUpdate(ch.Updates.SetFields) {
		info, err := rewriteItem(st, id, ch.Updates.SetFields)
		if err != nil {
			return itemError("ErrorItemNotFound")
		}
		id.MessageID, id.UID = info.ID, info.UID
		newID = oxews.EncodeItemID(id)
	}
	if err := applyReadFlag(st, id, ch.Updates.SetFields); err != nil {
		return itemError("ErrorItemNotFound")
	}
	return itemResponseMessage{
		ResponseClass: "Success", ResponseCode: "NoError",
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		Items: &itemsWrap{Messages: []oxews.Message{{ItemID: oxews.ItemIDElem{ID: newID, ChangeKey: oxews.ChangeKey(uint64(id.MessageID))}}}},
	}
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

// applyReadFlag writes the read flag when the request names it.
func applyReadFlag(st *objectstore.Store, id oxews.ItemID, fields []setItemField) error {
	for _, sf := range fields {
		if sf.FieldURI.URI != fieldIsRead {
			continue
		}
		flags, err := st.MessageFlags(id.FolderID, id.UID)
		if err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(sf.Message.IsRead), "true") {
			flags |= objectstore.FlagSeen
		} else {
			flags &^= objectstore.FlagSeen
		}
		if err := st.SetMessageFlags(id.FolderID, id.UID, flags); err != nil {
			return err
		}
	}
	return nil
}

// rewriteItem applies the content updates and stores the result in place of the
// original, returning the new message. The stored form is rebuilt through
// oxcmail.Export, the one proven outbound path, so an edited draft serializes
// exactly as a composed one does.
//
// The message is replaced rather than patched because a stored message is its
// serialized bytes; the same delete-then-append the webmail draft autosave uses.
// The new id is returned to the client in the response, so the client follows
// the message rather than holding an id that no longer resolves.
func rewriteItem(st *objectstore.Store, id oxews.ItemID, fields []setItemField) (objectstore.MessageInfo, error) {
	msg, err := st.OpenMessage(id.MessageID)
	if err != nil {
		return objectstore.MessageInfo{}, err
	}
	for _, sf := range fields {
		applyField(msg, sf)
	}
	oxcmail.EnsureMessageID(&msg.Props)
	raw, err := oxcmail.Export(msg, oxcmail.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		return objectstore.MessageInfo{}, err
	}
	flags, date := int64(0), time.Now()
	if info, err := st.MessageByUID(id.FolderID, id.UID); err == nil {
		flags, date = info.Flags, info.InternalDate
	}
	// The replacement is filed before the original is dropped, so a failure
	// between the two leaves a duplicate rather than nothing at all.
	info, err := st.AppendMessage(id.FolderID, raw, date, flags)
	if err != nil {
		return objectstore.MessageInfo{}, err
	}
	if err := st.DeleteMessage(id.FolderID, id.UID); err != nil {
		return objectstore.MessageInfo{}, err
	}
	return info, nil
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
	ItemIDs    struct {
		Items []refID `xml:"ItemId"`
	} `xml:"ItemIds"`
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
		id, err := oxews.DecodeItemID(ref.ID)
		if err != nil {
			msgs = append(msgs, itemError("ErrorInvalidRequest"))
			continue
		}
		// The id self-encodes its mailbox; a delegated item is gated on delete access.
		st, code := cache.openForItem(sess, id, mapi.FrightsDeleteAny)
		if code != "" {
			msgs = append(msgs, itemError(code))
			continue
		}
		var derr error
		switch req.DeleteType {
		case "HardDelete", "SoftDelete":
			derr = st.SoftDeleteMessage(id.FolderID, id.UID)
		default: // MoveToDeletedItems
			_, derr = moveMessage(st, id.FolderID, id.UID, int64(mapi.PrivateFIDDeletedItems))
		}
		if derr != nil {
			msgs = append(msgs, itemError("ErrorItemNotFound"))
			continue
		}
		msgs = append(msgs, itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"})
	}
	writeResponse(w, deleteItemResponse{Messages: msgs})
}

// --- MoveItem / CopyItem ---

type moveCopyItemRequest struct {
	ToFolderID folderRefs `xml:"ToFolderId"`
	ItemIDs    struct {
		Items []refID `xml:"ItemId"`
	} `xml:"ItemIds"`
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
	targets := resolveTargets(req.ToFolderID)
	if len(targets) == 0 {
		writeMoveCopy(w, remove, []itemResponseMessage{itemError("ErrorInvalidRequest")})
		return
	}
	if !targets[0].ok {
		writeMoveCopy(w, remove, []itemResponseMessage{itemError(targets[0].code)})
		return
	}
	toFID := targets[0].fid

	cache := s.newStoreCache()
	defer cache.closeAll()

	// Open and gate the destination once: a non-own target folder requires create access.
	destSt, _, destOwn, code := cache.open(sess, targets[0].mailbox)
	if code != "" {
		writeMoveCopy(w, remove, []itemResponseMessage{itemError(code)})
		return
	}
	if !destOwn {
		rights, err := destSt.ResolvePermission(toFID, sess.user)
		if err != nil {
			writeMoveCopy(w, remove, []itemResponseMessage{itemError("ErrorInternalServerError")})
			return
		}
		if rights&mapi.FrightsCreate == 0 {
			writeMoveCopy(w, remove, []itemResponseMessage{itemError("ErrorAccessDenied")})
			return
		}
	}
	destMailbox := ""
	if !destOwn {
		destMailbox = targets[0].mailbox
	}

	var msgs []itemResponseMessage
	for _, ref := range req.ItemIDs.Items {
		id, err := oxews.DecodeItemID(ref.ID)
		if err != nil {
			msgs = append(msgs, itemError("ErrorInvalidRequest"))
			continue
		}
		srcSt, _, srcOwn, code := cache.open(sess, id.Mailbox)
		if code != "" {
			msgs = append(msgs, itemError(code))
			continue
		}
		// The copy runs within a single store; moving an item across mailboxes is not
		// supported (the source and destination must be the same mailbox).
		if srcSt != destSt {
			msgs = append(msgs, itemError("ErrorAccessDenied"))
			continue
		}
		// A non-own source is gated on delete (move) or read (copy) of the source folder.
		if !srcOwn {
			need := mapi.FrightsReadAny
			if remove {
				need = mapi.FrightsDeleteAny
			}
			// checkItemAccess also binds the message to the folder that was
			// authorized. The recovery below addresses a soft-deleted message by id
			// alone, so without that binding a delegate could pair a folder they hold
			// rights on with a message deleted from a folder they cannot reach, and
			// restore it into a folder they can read.
			if code := checkItemAccess(srcSt, id, sess.user, need); code != "" {
				msgs = append(msgs, itemError(code))
				continue
			}
		}
		var info objectstore.MessageInfo
		if remove {
			// A soft-deleted source item (recovered from the Recoverable Items dumpster) has
			// no live uid, so MoveItem on it is a recovery into the chosen target folder; a
			// live item falls through to a normal move.
			info, err = destSt.RecoverMessageTo(id.MessageID, toFID)
			if errors.Is(err, objectstore.ErrNotFound) {
				info, err = moveMessage(destSt, id.FolderID, id.UID, toFID)
			}
		} else {
			info, err = copyMessage(destSt, id.FolderID, id.UID, toFID)
		}
		if err != nil {
			msgs = append(msgs, itemError("ErrorItemNotFound"))
			continue
		}
		newID := oxews.EncodeItemID(oxews.ItemID{FolderID: toFID, MessageID: info.ID, UID: info.UID, Mailbox: destMailbox})
		msgs = append(msgs, itemResponseMessage{
			ResponseClass: "Success", ResponseCode: "NoError",
			// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
			Items: &itemsWrap{Messages: []oxews.Message{{ItemID: oxews.ItemIDElem{ID: newID, ChangeKey: oxews.ChangeKey(uint64(info.ID))}}}},
		})
	}
	writeMoveCopy(w, remove, msgs)
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
