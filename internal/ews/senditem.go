package ews

import (
	"encoding/xml"
	"net/http"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// --- request ---

type sendItemRequest struct {
	SaveItemToFolder  string     `xml:"SaveItemToFolder,attr"`
	ItemIDs           []refID    `xml:"ItemIds>ItemId"`
	SavedItemFolderID folderRefs `xml:"SavedItemFolderId"`
}

// --- response ---

type sendItemResponse struct {
	XMLName  xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SendItemResponse"`
	Messages []itemResponseMessage `xml:"ResponseMessages>SendItemResponseMessage"`
}

// handleSendItem answers SendItem ([MS-OXWSCORE] 3.1.4.8): it transmits mail that
// was already composed and saved (a draft), addressed by ItemId. SaveItemToFolder
// chooses whether a copy is filed, into SavedItemFolderId, or Sent Items by
// default; pairing SaveItemToFolder=false with a SavedItemFolderId is the
// documented contradiction ErrorInvalidSendItemSaveSettings.
//
// On a successful send the source draft is consumed: saving leaves only the filed
// copy, not saving drops it, so a sent message never lingers in its folder looking
// unsent. (The reference files the copy back into the draft's own folder rather
// than the save folder; that contradicts the spec, so this follows the spec.)
func (s *Server) handleSendItem(w http.ResponseWriter, inner []byte, sess *session) {
	var req sendItemRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "SendItem: invalid request", err)
		return
	}
	save := strings.EqualFold(req.SaveItemToFolder, "true") || req.SaveItemToFolder == "1"
	saveFID, code := sendItemSaveFolder(req, save)
	if code != "" {
		writeResponse(w, sendItemResponse{Messages: []itemResponseMessage{itemError(code)}})
		return
	}

	cache := s.newStoreCache()
	defer cache.closeAll()

	msgs := make([]itemResponseMessage, 0, len(req.ItemIDs))
	for _, ref := range req.ItemIDs {
		msgs = append(msgs, s.sendOne(cache, sess, ref.ID, save, saveFID))
	}
	writeResponse(w, sendItemResponse{Messages: msgs})
}

// sendItemSaveFolder resolves the folder the sent copy is filed into. Naming a
// folder while asking for no copy is a contradiction the protocol rejects, and
// the default is Sent Items. A non-empty code refuses the request.
func sendItemSaveFolder(req sendItemRequest, save bool) (int64, string) {
	hasSaveFolder := len(req.SavedItemFolderID.Distinguished) > 0 || len(req.SavedItemFolderID.Folders) > 0
	if !save && hasSaveFolder {
		return 0, "ErrorInvalidSendItemSaveSettings"
	}
	if !hasSaveFolder {
		return int64(mapi.PrivateFIDSentItems), ""
	}
	targets := resolveTargets(req.SavedItemFolderID)
	if len(targets) == 0 {
		return 0, "ErrorInvalidRequest"
	}
	if !targets[0].ok {
		return 0, targets[0].code
	}
	return targets[0].fid, ""
}

// sendOne transmits one saved draft and settles its fate, returning the per-item
// response message. The SMTP envelope takes every routable recipient (To+Cc+Bcc),
// while the transmitted copy is rebuilt through oxcmail.Export (the one proven
// outbound path) with Bcc bags dropped, Export writes a Bcc header for any Bcc
// bag, so leaving them in the wire copy would disclose blind recipients to the
// To/Cc readers.
func (s *Server) sendOne(cache *storeCache, sess *session, itemID string, save bool, saveFID int64) itemResponseMessage {
	id, err := oxews.DecodeItemID(itemID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	// Open the draft's mailbox. Sending another mailbox's draft (send-on-behalf: the
	// draft already carries the principal's From) is gated on read access to its folder;
	// filing the sent copy needs create access to the save folder. Both gates and the
	// filed copy resolve in the draft's own mailbox.
	st, _, isOwn, code := cache.open(sess, id.Mailbox)
	if code != "" {
		return itemError(code)
	}
	if code := checkSendAccess(st, sess, id, isOwn, save, saveFID); code != "" {
		return itemError(code)
	}
	msg, err := st.OpenMessage(id.MessageID)
	if err != nil {
		return itemError("ErrorItemNotFound")
	}

	recips, wire := splitRecipients(msg.Recipients)
	if len(recips) == 0 {
		return itemError("ErrorInvalidRecipients")
	}
	msg.Recipients = wire
	oxcmail.EnsureMessageID(&msg.Props)
	raw, err := oxcmail.Export(msg, oxcmail.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		return itemError("ErrorInternalServerError")
	}
	if _, err := mta.DeliverAndRelay(s.accounts, s.Spool, sess.user, recips, raw, time.Now()); err != nil {
		return itemError("ErrorInternalServerError")
	}

	if err := consumeDraft(st, id, raw, save, saveFID); err != nil {
		return itemError("ErrorInternalServerError")
	}
	return itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"}
}

// consumeDraft files the sent copy and drops the original draft. The copy is
// filed first, so a later delete failure never loses the sent record.
func consumeDraft(st *objectstore.Store, id oxews.ItemID, raw []byte, save bool, saveFID int64) error {
	if save {
		if _, err := st.AppendMessage(saveFID, raw, time.Now(), objectstore.FlagSeen); err != nil {
			return err
		}
	}
	return st.DeleteMessage(id.FolderID, id.UID)
}

// checkSendAccess gates sending another mailbox's draft (send-on-behalf: the
// draft already carries the principal's From) on read access to its folder, and
// filing the sent copy on create access to the save folder. An empty code means
// the send may proceed.
func checkSendAccess(st *objectstore.Store, sess *session, id oxews.ItemID, isOwn, save bool, saveFID int64) string {
	if isOwn {
		return ""
	}
	if code := checkItemAccess(st, id, sess.user, mapi.FrightsReadAny); code != "" {
		return code
	}
	if !save {
		return ""
	}
	return folderCreateAccess(st, saveFID, sess.user)
}

// splitRecipients returns the routable addresses the message is delivered to and
// the recipient bags that ride on the wire, which exclude Bcc so recipients never
// see the blind list.
func splitRecipients(bags []mapi.PropertyValues) (recips []string, wire []mapi.PropertyValues) {
	wire = make([]mapi.PropertyValues, 0, len(bags))
	for _, bag := range bags {
		if addr := recipientSMTP(bag); addr != "" {
			recips = append(recips, addr)
		}
		if rt, _ := bag.Get(mapi.PrRecipientType); rt != int32(mapi.RecipBcc) {
			wire = append(wire, bag)
		}
	}
	return recips, wire
}

// recipientSMTP extracts a routable SMTP address from a recipient bag: the
// explicit PR_SMTP_ADDRESS, else PR_EMAIL_ADDRESS when the address type is SMTP.
// X500/EX recipients carry no SMTP address and yield "".
func recipientSMTP(bag mapi.PropertyValues) string {
	if v, ok := bag.Get(mapi.PrSmtpAddress); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	if v, ok := bag.Get(mapi.PrAddrType); ok {
		if at, _ := v.(string); strings.EqualFold(at, "SMTP") {
			if e, ok := bag.Get(mapi.PrEmailAddress); ok {
				if s, _ := e.(string); s != "" {
					return s
				}
			}
		}
	}
	return ""
}
