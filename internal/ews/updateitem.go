package ews

import (
	"encoding/xml"
	"net/http"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
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
	Message struct {
		IsRead string `xml:"IsRead"`
	} `xml:"Message"`
}

type updateItemResponse struct {
	XMLName  xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateItemResponse"`
	Messages []itemResponseMessage `xml:"ResponseMessages>UpdateItemResponseMessage"`
}

// handleUpdateItem answers UpdateItem. v1 honors the message:IsRead field
// (read/unread toggle → SetMessageFlags); other SetItemField updates are
// accepted but ignored. Categories and arbitrary property updates are deferred.
func (s *Server) handleUpdateItem(w http.ResponseWriter, inner []byte, sess *session) {
	var req updateItemRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "UpdateItem: "+err.Error())
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()

	var msgs []itemResponseMessage
	for _, ch := range req.ItemChanges.Changes {
		id, err := oxews.DecodeItemID(ch.ItemID.ID)
		if err != nil {
			msgs = append(msgs, itemError("ErrorInvalidRequest"))
			continue
		}
		// The id self-encodes its mailbox; a delegated item is gated on edit access.
		st, code := cache.openForItem(sess, id, mapi.FrightsEditAny)
		if code != "" {
			msgs = append(msgs, itemError(code))
			continue
		}
		failed := false
		for _, sf := range ch.Updates.SetFields {
			if sf.FieldURI.URI != "message:IsRead" {
				continue
			}
			flags, err := st.MessageFlags(id.FolderID, id.UID)
			if err != nil {
				failed = true
				break
			}
			if strings.EqualFold(strings.TrimSpace(sf.Message.IsRead), "true") {
				flags |= objectstore.FlagSeen
			} else {
				flags &^= objectstore.FlagSeen
			}
			if err := st.SetMessageFlags(id.FolderID, id.UID, flags); err != nil {
				failed = true
				break
			}
		}
		if failed {
			msgs = append(msgs, itemError("ErrorItemNotFound"))
			continue
		}
		msgs = append(msgs, itemResponseMessage{
			ResponseClass: "Success", ResponseCode: "NoError",
			Items: &itemsWrap{Messages: []oxews.Message{{ItemID: oxews.ItemIDElem{ID: ch.ItemID.ID, ChangeKey: oxews.ChangeKey(uint64(id.MessageID))}}}},
		})
	}
	writeResponse(w, updateItemResponse{Messages: msgs})
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
		writeSOAPFault(w, "ErrorInvalidRequest", "DeleteItem: "+err.Error())
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
		writeSOAPFault(w, "ErrorInvalidRequest", "Move/CopyItem: "+err.Error())
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
			rights, err := srcSt.ResolvePermission(id.FolderID, sess.user)
			if err != nil {
				msgs = append(msgs, itemError("ErrorInternalServerError"))
				continue
			}
			if rights&need == 0 {
				msgs = append(msgs, itemError("ErrorAccessDenied"))
				continue
			}
		}
		var info objectstore.MessageInfo
		if remove {
			info, err = moveMessage(destSt, id.FolderID, id.UID, toFID)
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
