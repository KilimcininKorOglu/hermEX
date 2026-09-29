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
	// SendMeetingInvitations says whether a created meeting's request goes to its
	// attendees: SendToNone, SendOnlyToAll or SendToAllAndSaveCopy.
	SendMeetingInvitations string `xml:"SendMeetingInvitations,attr"`
	// SavedItemFolderID names the folder a created calendar item is stored in.
	SavedItemFolderID folderRefs `xml:"SavedItemFolderId"`
	Items             struct {
		Messages          []createMessage      `xml:"Message"`
		CalendarItems     []createCalendarItem `xml:"CalendarItem"`
		Accept            []meetingResponse    `xml:"AcceptItem"`
		TentativelyAccept []meetingResponse    `xml:"TentativelyAcceptItem"`
		Decline           []meetingResponse    `xml:"DeclineItem"`
		// The smart-response types ([MS-OXWSMSG] ReplyToItemType and its siblings).
		// A client sends one of these instead of a plain Message when the user
		// replies to or forwards a message; saving such a draft is the common case,
		// and dropping the element would store nothing and report nothing.
		ReplyToItem    []smartResponse `xml:"ReplyToItem"`
		ReplyAllToItem []smartResponse `xml:"ReplyAllToItem"`
		ForwardItem    []smartResponse `xml:"ForwardItem"`
		// SuppressReadReceipt declines the read receipt a received message asks for.
		SuppressReadReceipt []struct {
			ReferenceItemID refID `xml:"ReferenceItemId"`
		} `xml:"SuppressReadReceipt"`
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
	// Extended are the MAPI properties the client sets by field URI. They are kept
	// on the stored copy; the MIME form sent to the recipients has no room for them.
	Extended []oxews.ExtendedProperty `xml:"ExtendedProperty"`
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
	msgs = append(msgs, s.createCalendarItems(st, sess, req)...)
	msgs = append(msgs, s.createMeetingResponses(st, sess, req, send, save)...)
	msgs = append(msgs, s.createReceiptSuppressions(sess, req)...)
	writeResponse(w, createItemResponse{Messages: msgs})
}

// createReceiptSuppressions answers the SuppressReadReceipt response objects in the
// request, each of which declines one message's read receipt.
func (s *Server) createReceiptSuppressions(sess *session, req createItemRequest) []itemResponseMessage {
	if len(req.Items.SuppressReadReceipt) == 0 {
		return nil
	}
	cache := s.newStoreCache()
	defer cache.closeAll()
	msgs := make([]itemResponseMessage, 0, len(req.Items.SuppressReadReceipt))
	for _, sr := range req.Items.SuppressReadReceipt {
		msgs = append(msgs, s.suppressReadReceipt(cache, sess, sr.ReferenceItemID))
	}
	return msgs
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
// (when the disposition sends) notifies the organizer with an iTIP REPLY. A
// SendAndSaveCopy response is kept in the caller's own Sent Items, st, as any
// other message sent with that disposition is ([MS-OXWSCDATA] MessageDispositionType).
func (s *Server) createMeetingResponses(st *objectstore.Store, sess *session, req createItemRequest, send, save bool) []itemResponseMessage {
	var sentCopy func([]byte)
	if send && save {
		sentCopy = sentItemsCopy(st)
	}
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
			msgs = append(msgs, s.meetingRespond(sess, mr, r.response, send, sentCopy))
		}
	}
	return msgs
}

// createOneItem builds one outgoing message, sends it when the disposition asks,
// and files the copy the disposition asks for.
func (s *Server) createOneItem(st *objectstore.Store, sess *session, m createMessage,
	disp string, send, save bool) itemResponseMessage {
	// A From the caller holds no send-as or send-on-behalf right to is refused with
	// the code Exchange gives it, which tells the client the sending account is the
	// problem rather than access to the folder.
	representing, sender, ok := s.resolveSender(sess.user, m.From.Mailbox.EmailAddress)
	if !ok {
		return itemError("ErrorSendAsDenied")
	}
	ext, code := extendedValues(st, m.Extended)
	if code != "" {
		return itemError(code)
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
		code, keepOwnCopy := s.sendCreatedItem(sess, m, raw)
		if code != "" {
			return itemError(code)
		}
		// The mailbox the message was sent in the name of may have filed the only copy.
		save = save && keepOwnCopy
	}
	// Every successful CreateItemResponseMessage carries an <m:Items> container,
	// clients reject its absence. It is empty for SendOnly (nothing is persisted)
	// and holds the stored item's id (with a ChangeKey, as every other returned
	// ItemId does) for SaveOnly and SendAndSaveCopy.
	rm := itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError", Items: &itemsWrap{}}
	if !save {
		return rm
	}
	if err := fileCreatedItem(st, raw, disp, ext, rm.Items); err != nil {
		// A draft that was not stored is the whole outcome of SaveOnly, so it fails.
		// The Sent copy of SendAndSaveCopy is filed after the message went out, and a
		// failure there must not tell the client to send it again; it is recorded.
		if !send {
			return itemError("ErrorItemSave")
		}
		st.LogSwallowedError("ews.file_sent_copy", err)
	}
	return rm
}

// sendCreatedItem relays one built message to its recipients, reporting the
// response code refusing the send; an empty code means it went out. keepOwnCopy is
// false when the mailbox the message was sent in the name of filed the only copy.
func (s *Server) sendCreatedItem(sess *session, m createMessage, raw []byte) (code string, keepOwnCopy bool) {
	recips := recipientEmails(m)
	if len(recips) == 0 {
		return "ErrorInvalidRecipients", false
	}
	_, keepOwnCopy, err := mta.SendAndRelay(s.accounts, s.Spool, sess.user, recips, raw, time.Now())
	if err != nil {
		return "ErrorInternalServerError", false
	}
	return "", keepOwnCopy
}

// fileCreatedItem stores the copy the disposition asks for: a draft for SaveOnly,
// a sent copy otherwise, and records its id in the response.
func fileCreatedItem(st *objectstore.Store, raw []byte, disp string, ext mapi.PropertyValues, items *itemsWrap) error {
	folder := int64(mapi.PrivateFIDSentItems)
	flags := int64(objectstore.FlagSeen)
	if disp == "SaveOnly" {
		folder = int64(mapi.PrivateFIDDraft)
		flags = objectstore.FlagDraft
	}
	info, err := st.AppendMessage(folder, raw, time.Now(), flags)
	if err != nil {
		return err
	}
	if info, err = storeExtended(st, info, ext); err != nil {
		return err
	}
	id := oxews.EncodeItemID(oxews.ItemID{FolderID: folder, MessageID: info.ID, UID: info.UID})
	items.Messages = []oxews.Message{{ItemID: oxews.ItemIDElem{ID: id, ChangeKey: changeKey(st, info.ID)}}}
	return nil
}

// storeExtended writes the client's extended properties onto a filed message and
// indexes it again, so its IMAP form is read from the properties it now holds.
func storeExtended(st *objectstore.Store, info objectstore.MessageInfo, ext mapi.PropertyValues) (objectstore.MessageInfo, error) {
	if len(ext) == 0 {
		return info, nil
	}
	if err := st.SetMessageProperties(info.ID, ext); err != nil {
		return info, err
	}
	return st.ReindexMessage(info.ID)
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
