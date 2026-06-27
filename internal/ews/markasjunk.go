package ews

import (
	"encoding/xml"
	"net/http"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/oxews"
)

// Junk handling (MS-OXWSCORE MarkAsJunk) adds or removes a message's sender from
// the mailbox's blocked-sender list and optionally moves the message to or from
// the Junk Email folder. IsJunk true blocks the sender (and moves to Junk when
// MoveItem is set); IsJunk false unblocks (and moves to the Inbox). The blocked
// list is the recipient's own personal allow/block rules, the same set the MTA
// consults at delivery and webmail2 manages. v1 acts only on the caller's own
// mailbox; a delegated item is refused, since the block list is the caller's own.

// junkRuleStore is the directory capability of editing a mailbox owner's personal
// blocked-sender list. A directory without it (a static test directory) still
// moves the item; only the block-list change is skipped.
type junkRuleStore interface {
	SetRecipientRule(username, pattern, action string) error
	DeleteRecipientRule(username, pattern string) (bool, error)
}

type markAsJunkRequest struct {
	IsJunk   bool `xml:"IsJunk,attr"`
	MoveItem bool `xml:"MoveItem,attr"`
	ItemIDs  struct {
		IDs []refID `xml:"ItemId"`
	} `xml:"ItemIds"`
}

type markAsJunkResponse struct {
	XMLName  xml.Name                    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MarkAsJunkResponse"`
	Messages []markAsJunkResponseMessage `xml:"ResponseMessages>MarkAsJunkResponseMessage"`
}

type markAsJunkResponseMessage struct {
	ResponseClass string       `xml:"ResponseClass,attr"`
	ResponseCode  string       `xml:"ResponseCode"`
	MovedItemID   *movedItemID `xml:"MovedItemId,omitempty"`
}

type movedItemID struct {
	ID        string `xml:"Id,attr"`
	ChangeKey string `xml:"ChangeKey,attr"`
}

// handleMarkAsJunk answers MarkAsJunk: each item's sender is blocked or unblocked
// and the item is optionally moved to Junk or the Inbox.
func (s *Server) handleMarkAsJunk(w http.ResponseWriter, inner []byte, sess *session) {
	var req markAsJunkRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "MarkAsJunk: "+err.Error())
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()

	var msgs []markAsJunkResponseMessage
	for _, ref := range req.ItemIDs.IDs {
		msgs = append(msgs, s.markOneAsJunk(cache, sess, ref.ID, req.IsJunk, req.MoveItem))
	}
	writeResponse(w, markAsJunkResponse{Messages: msgs})
}

// markOneAsJunk applies the junk action to one item.
func (s *Server) markOneAsJunk(cache *storeCache, sess *session, itemID string, isJunk, moveItem bool) markAsJunkResponseMessage {
	id, err := oxews.DecodeItemID(itemID)
	if err != nil {
		return markJunkErr("ErrorInvalidId")
	}
	if id.Mailbox != "" {
		// The block list is the caller's own; v1 does not act on a delegated mailbox.
		return markJunkErr("ErrorAccessDenied")
	}
	st, code := cache.openForItem(sess, id, mapi.FrightsEditAny)
	if code != "" {
		return markJunkErr(code)
	}

	msg, err := st.OpenMessage(id.MessageID)
	if err != nil {
		return markJunkErr("ErrorItemNotFound")
	}

	if sender := senderSMTP(msg.Props); sender != "" {
		if ruleStore, ok := s.accounts.(junkRuleStore); ok {
			if isJunk {
				_ = ruleStore.SetRecipientRule(sess.user, sender, directory.SenderBlock)
			} else {
				_, _ = ruleStore.DeleteRecipientRule(sess.user, sender)
			}
		}
	}

	resp := markAsJunkResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"}
	if moveItem {
		target := int64(mapi.PrivateFIDInbox)
		if isJunk {
			target = int64(mapi.PrivateFIDJunk)
		}
		moved, err := moveMessage(st, id.FolderID, id.UID, target)
		if err != nil {
			return markJunkErr("ErrorItemNotFound")
		}
		resp.MovedItemID = &movedItemID{
			ID:        oxews.EncodeItemID(oxews.ItemID{FolderID: target, MessageID: moved.ID, UID: moved.UID}),
			ChangeKey: oxews.ChangeKey(uint64(moved.ID)),
		}
	}
	return resp
}

// markJunkErr builds an error response message.
func markJunkErr(code string) markAsJunkResponseMessage {
	return markAsJunkResponseMessage{ResponseClass: "Error", ResponseCode: code}
}

// senderSMTP extracts a message's sender SMTP address, preferring the sender, then
// the represented sender, then the generic sender address. A non-SMTP address (an
// internal X.500/EX address with no "@") is skipped, since the block list keys on
// SMTP addresses.
func senderSMTP(props mapi.PropertyValues) string {
	for _, tag := range []mapi.PropTag{mapi.PrSenderSmtpAddress, mapi.PrSentRepresentingSmtpAddress, mapi.PrSenderEmailAddress} {
		if v, ok := props.Get(tag); ok {
			if s, ok := v.(string); ok {
				s = strings.ToLower(strings.TrimSpace(s))
				if strings.Contains(s, "@") {
					return s
				}
			}
		}
	}
	return ""
}
