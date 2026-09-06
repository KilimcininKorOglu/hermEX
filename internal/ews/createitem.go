package ews

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"net/http"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/meeting"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// --- request ---

type createItemRequest struct {
	MessageDisposition string `xml:"MessageDisposition,attr"`
	Items              struct {
		Messages          []createMessage   `xml:"Message"`
		Accept            []meetingResponse `xml:"AcceptItem"`
		TentativelyAccept []meetingResponse `xml:"TentativelyAcceptItem"`
		Decline           []meetingResponse `xml:"DeclineItem"`
	} `xml:"Items"`
}

type createMessage struct {
	Subject string `xml:"Subject"`
	Body    struct {
		Type    string `xml:"BodyType,attr"`
		Content string `xml:",chardata"`
	} `xml:"Body"`
	ToRecipients  mailboxList `xml:"ToRecipients"`
	CcRecipients  mailboxList `xml:"CcRecipients"`
	BccRecipients mailboxList `xml:"BccRecipients"`
}

type mailboxList struct {
	Mailbox []mailboxEntry `xml:"Mailbox"`
}

type mailboxEntry struct {
	Name         string `xml:"Name"`
	EmailAddress string `xml:"EmailAddress"`
}

// --- response ---

type createItemResponse struct {
	XMLName  xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateItemResponse"`
	Messages []itemResponseMessage `xml:"ResponseMessages>CreateItemResponseMessage"`
}

// handleCreateItem answers CreateItem. The disposition selects send and/or save:
// SendOnly delivers; SaveOnly stores a draft; SendAndSaveCopy delivers and files
// a Sent copy. The message is built into an IPM.Note and rendered by
// oxcmail.Export (never hand-rolled MIME). Bcc recipients are delivered but kept
// off the wire, the delivery message carries only To/Cc bags.
func (s *Server) handleCreateItem(w http.ResponseWriter, inner []byte, sess *session) {
	var req createItemRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "CreateItem: invalid request", err)
		return
	}
	disp := req.MessageDisposition
	if disp == "" {
		disp = "SaveOnly"
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		s.soapFault(w, "ErrorInternalServerError", "an internal error occurred", err)
		return
	}
	defer st.Close()

	send := disp == "SendOnly" || disp == "SendAndSaveCopy"
	save := disp == "SaveOnly" || disp == "SendAndSaveCopy"

	var msgs []itemResponseMessage
	for _, m := range req.Items.Messages {
		msgs = append(msgs, s.createOneItem(st, sess, m, disp, send, save))
	}

	// Meeting responses ([MS-OXWSMTGS]): an Accept/Tentative/Decline answers the
	// referenced meeting request, updating the attendee's calendar and the request,
	// and (when the disposition sends) notifying the organizer with an iTIP REPLY.
	for _, mr := range req.Items.Accept {
		msgs = append(msgs, s.meetingRespond(sess, mr.ReferenceItemID, meeting.ResponseAccepted, send))
	}
	for _, mr := range req.Items.TentativelyAccept {
		msgs = append(msgs, s.meetingRespond(sess, mr.ReferenceItemID, meeting.ResponseTentative, send))
	}
	for _, mr := range req.Items.Decline {
		msgs = append(msgs, s.meetingRespond(sess, mr.ReferenceItemID, meeting.ResponseDeclined, send))
	}
	writeResponse(w, createItemResponse{Messages: msgs})
}

// createOneItem builds one outgoing message, sends it when the disposition asks,
// and files the copy the disposition asks for.
func (s *Server) createOneItem(st *objectstore.Store, sess *session, m createMessage,
	disp string, send, save bool) itemResponseMessage {
	out := oxews.BuildOutgoing(oxews.OutgoingInput{
		From:      sess.user,
		Subject:   m.Subject,
		Body:      m.Body.Content,
		BodyType:  m.Body.Type,
		To:        toMailboxes(m.ToRecipients),
		Cc:        toMailboxes(m.CcRecipients),
		MessageID: newMessageID(s.hostname),
		Sent:      time.Now(),
	})
	raw, err := oxcmail.Export(out, oxcmail.Options{})
	if err != nil {
		return itemError("ErrorInternalServerError")
	}
	if send {
		if code := s.sendCreatedItem(sess, m, raw); code != "" {
			return itemError(code)
		}
	}
	// Every successful CreateItemResponseMessage carries an <m:Items> container,
	// clients reject its absence. It is empty for SendOnly (nothing is persisted)
	// and holds the stored item's id (with a ChangeKey, as every other returned
	// ItemId does) for SaveOnly and SendAndSaveCopy.
	rm := itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError", Items: &itemsWrap{}}
	if save {
		fileCreatedItem(st, raw, disp, rm.Items)
	}
	return rm
}

// sendCreatedItem relays one built message to its recipients, reporting the
// response code refusing the send; an empty code means it went out.
func (s *Server) sendCreatedItem(sess *session, m createMessage, raw []byte) string {
	recips := recipientEmails(m)
	if len(recips) == 0 {
		return "ErrorInvalidRecipients"
	}
	if _, err := mta.DeliverAndRelay(s.accounts, s.Spool, sess.user, recips, raw, time.Now()); err != nil {
		return "ErrorInternalServerError"
	}
	return ""
}

// fileCreatedItem stores the copy the disposition asks for: a draft for SaveOnly,
// a sent copy otherwise, and records its id in the response.
func fileCreatedItem(st *objectstore.Store, raw []byte, disp string, items *itemsWrap) {
	folder := int64(mapi.PrivateFIDSentItems)
	flags := int64(objectstore.FlagSeen)
	if disp == "SaveOnly" {
		folder = int64(mapi.PrivateFIDDraft)
		flags = objectstore.FlagDraft
	}
	info, err := st.AppendMessage(folder, raw, time.Now(), flags)
	if err != nil {
		return
	}
	id := oxews.EncodeItemID(oxews.ItemID{FolderID: folder, MessageID: info.ID, UID: info.UID})
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	items.Messages = []oxews.Message{{ItemID: oxews.ItemIDElem{ID: id, ChangeKey: oxews.ChangeKey(uint64(info.ID))}}}
}

// itemError builds an error response message with the given EWS response code.
func itemError(code string) itemResponseMessage {
	return itemResponseMessage{ResponseClass: "Error", ResponseCode: code}
}

// toMailboxes converts request mailboxes to oxews mailboxes.
func toMailboxes(list mailboxList) []oxews.Mailbox {
	out := make([]oxews.Mailbox, 0, len(list.Mailbox))
	for _, m := range list.Mailbox {
		out = append(out, oxews.Mailbox{Name: m.Name, EmailAddress: m.EmailAddress})
	}
	return out
}

// recipientEmails collects every To/Cc/Bcc address (Bcc is delivered but never
// placed on the wire copy).
func recipientEmails(m createMessage) []string {
	var out []string
	for _, list := range []mailboxList{m.ToRecipients, m.CcRecipients, m.BccRecipients} {
		for _, mb := range list.Mailbox {
			if mb.EmailAddress != "" {
				out = append(out, mb.EmailAddress)
			}
		}
	}
	return out
}

// newMessageID mints an opaque RFC 5322 Message-ID for an outgoing message.
func newMessageID(host string) string {
	if host == "" {
		host = "hermex"
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "<" + hex.EncodeToString(b) + "@" + host + ">"
}
