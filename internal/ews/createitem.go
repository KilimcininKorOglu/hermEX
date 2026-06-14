package ews

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"net/http"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// --- request ---

type createItemRequest struct {
	MessageDisposition string `xml:"MessageDisposition,attr"`
	Items              struct {
		Messages []createMessage `xml:"Message"`
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
// off the wire — the delivery message carries only To/Cc bags.
func (s *Server) handleCreateItem(w http.ResponseWriter, inner []byte, sess *session) {
	var req createItemRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "CreateItem: "+err.Error())
		return
	}
	disp := req.MessageDisposition
	if disp == "" {
		disp = "SaveOnly"
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	send := disp == "SendOnly" || disp == "SendAndSaveCopy"
	save := disp == "SaveOnly" || disp == "SendAndSaveCopy"

	var msgs []itemResponseMessage
	for _, m := range req.Items.Messages {
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
			msgs = append(msgs, itemError("ErrorInternalServerError"))
			continue
		}

		if send {
			recips := recipientEmails(m)
			if len(recips) == 0 {
				msgs = append(msgs, itemError("ErrorInvalidRecipients"))
				continue
			}
			if _, err := mta.Deliver(s.accounts, sess.user, recips, raw, time.Now()); err != nil {
				msgs = append(msgs, itemError("ErrorInternalServerError"))
				continue
			}
		}

		rm := itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"}
		if save {
			folder := int64(mapi.PrivateFIDSentItems)
			flags := int64(objectstore.FlagSeen)
			if disp == "SaveOnly" {
				folder = int64(mapi.PrivateFIDDraft)
				flags = objectstore.FlagDraft
			}
			if info, err := st.AppendMessage(folder, raw, time.Now(), flags); err == nil {
				id := oxews.EncodeItemID(oxews.ItemID{FolderID: folder, MessageID: info.ID, UID: info.UID})
				rm.Items = &itemsWrap{Messages: []oxews.Message{{ItemID: oxews.ItemIDElem{ID: id}}}}
			}
		}
		msgs = append(msgs, rm)
	}
	writeResponse(w, createItemResponse{Messages: msgs})
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
