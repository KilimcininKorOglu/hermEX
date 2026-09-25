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
		// The smart-response types ([MS-OXWSMSG] ReplyToItemType and its siblings).
		// A client sends one of these instead of a plain Message when the user
		// replies to or forwards a message; saving such a draft is the common case,
		// and dropping the element would store nothing and report nothing.
		ReplyToItem    []smartResponse `xml:"ReplyToItem"`
		ReplyAllToItem []smartResponse `xml:"ReplyAllToItem"`
		ForwardItem    []smartResponse `xml:"ForwardItem"`
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
	// From is the identity the client chose to write as. It is authorized before it
	// is used, never trusted as sent.
	From struct {
		Mailbox mailboxEntry `xml:"Mailbox"`
	} `xml:"From"`
}

// smartResponse is one reply or forward item. Outlook for Mac nests a full Message
// element inside the smart-response element, while the published schema puts the
// message fields on the element itself; both shapes are read, and the nested one wins
// when it is present.
type smartResponse struct {
	ReferenceItemID refID          `xml:"ReferenceItemId"`
	Message         *createMessage `xml:"Message"`
	createMessage                  // the flat shape: the message fields on the element itself
}

// message returns the message body a smart response carries, preferring the nested
// Message element over the fields written directly on the smart-response element.
func (s smartResponse) message() createMessage {
	if s.Message != nil {
		return *s.Message
	}
	return s.createMessage
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

	msgs := s.createMessages(st, sess, req, disp, send, save)
	msgs = append(msgs, s.createMeetingResponses(sess, req, send)...)
	writeResponse(w, createItemResponse{Messages: msgs})
}

// createMessages stores every message-shaped item in the request: a plain Message and the
// smart-response elements a client sends when the user replies to or forwards a message.
// A reply or forward stores the same IPM.Note, so its body takes the same path.
func (s *Server) createMessages(st *objectstore.Store, sess *session, req createItemRequest,
	disp string, send, save bool) []itemResponseMessage {
	var msgs []itemResponseMessage
	for _, m := range req.Items.Messages {
		msgs = append(msgs, s.createOneItem(st, sess, m, disp, send, save))
	}
	for _, list := range [][]smartResponse{req.Items.ReplyToItem, req.Items.ReplyAllToItem, req.Items.ForwardItem} {
		for _, sr := range list {
			msgs = append(msgs, s.createOneItem(st, sess, sr.message(), disp, send, save))
		}
	}
	return msgs
}

// createMeetingResponses answers the meeting responses in the request ([MS-OXWSMTGS]): an
// Accept/Tentative/Decline updates the attendee's calendar and the referenced request, and
// (when the disposition sends) notifies the organizer with an iTIP REPLY.
func (s *Server) createMeetingResponses(sess *session, req createItemRequest, send bool) []itemResponseMessage {
	var msgs []itemResponseMessage
	for _, r := range []struct {
		items    []meetingResponse
		response int32
	}{
		{req.Items.Accept, meeting.ResponseAccepted},
		{req.Items.TentativelyAccept, meeting.ResponseTentative},
		{req.Items.Decline, meeting.ResponseDeclined},
	} {
		for _, mr := range r.items {
			msgs = append(msgs, s.meetingRespond(sess, mr.ReferenceItemID, r.response, send))
		}
	}
	return msgs
}

// createOneItem builds one outgoing message, sends it when the disposition asks,
// and files the copy the disposition asks for.
func (s *Server) createOneItem(st *objectstore.Store, sess *session, m createMessage,
	disp string, send, save bool) itemResponseMessage {
	representing, sender, ok := s.resolveSender(sess.user, m.From.Mailbox.EmailAddress)
	if !ok {
		return itemError("ErrorAccessDenied")
	}
	out := oxews.BuildOutgoing(oxews.OutgoingInput{
		From:      representing,
		Sender:    sender,
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
	items.Messages = []oxews.Message{{ItemID: oxews.ItemIDElem{ID: id, ChangeKey: changeKey(st, info.ID)}}}
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
