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
// chooses whether a copy is filed — into SavedItemFolderId, or Sent Items by
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
		writeSOAPFault(w, "ErrorInvalidRequest", "SendItem: "+err.Error())
		return
	}
	save := strings.EqualFold(req.SaveItemToFolder, "true") || req.SaveItemToFolder == "1"
	hasSaveFolder := len(req.SavedItemFolderID.Distinguished) > 0 || len(req.SavedItemFolderID.Folders) > 0

	if !save && hasSaveFolder {
		writeResponse(w, sendItemResponse{Messages: []itemResponseMessage{itemError("ErrorInvalidSendItemSaveSettings")}})
		return
	}

	saveFID := int64(mapi.PrivateFIDSentItems)
	if hasSaveFolder {
		targets := resolveTargets(req.SavedItemFolderID)
		if len(targets) == 0 || !targets[0].ok {
			code := "ErrorInvalidRequest"
			if len(targets) > 0 {
				code = targets[0].code
			}
			writeResponse(w, sendItemResponse{Messages: []itemResponseMessage{itemError(code)}})
			return
		}
		saveFID = targets[0].fid
	}

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	msgs := make([]itemResponseMessage, 0, len(req.ItemIDs))
	for _, ref := range req.ItemIDs {
		msgs = append(msgs, s.sendOne(st, sess, ref.ID, save, saveFID))
	}
	writeResponse(w, sendItemResponse{Messages: msgs})
}

// sendOne transmits one saved draft and settles its fate, returning the per-item
// response message. The SMTP envelope takes every routable recipient (To+Cc+Bcc),
// while the transmitted copy is rebuilt through oxcmail.Export (the one proven
// outbound path) with Bcc bags dropped — Export writes a Bcc header for any Bcc
// bag, so leaving them in the wire copy would disclose blind recipients to the
// To/Cc readers.
func (s *Server) sendOne(st *objectstore.Store, sess *session, itemID string, save bool, saveFID int64) itemResponseMessage {
	id, err := oxews.DecodeItemID(itemID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	msg, err := st.OpenMessage(id.MessageID)
	if err != nil {
		return itemError("ErrorItemNotFound")
	}

	var recips []string
	wire := make([]mapi.PropertyValues, 0, len(msg.Recipients))
	for _, bag := range msg.Recipients {
		if addr := recipientSMTP(bag); addr != "" {
			recips = append(recips, addr)
		}
		if rt, _ := bag.Get(mapi.PrRecipientType); rt != int32(mapi.RecipBcc) {
			wire = append(wire, bag)
		}
	}
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

	// Sent — consume the draft. File the copy first (so a later delete failure
	// never loses the sent record), then drop the original.
	if save {
		if _, err := st.AppendMessage(saveFID, raw, time.Now(), objectstore.FlagSeen); err != nil {
			return itemError("ErrorInternalServerError")
		}
	}
	if err := st.DeleteMessage(id.FolderID, id.UID); err != nil {
		return itemError("ErrorInternalServerError")
	}
	return itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"}
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
