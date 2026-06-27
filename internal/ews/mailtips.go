package ews

import (
	"encoding/xml"
	"net/http"
	"strings"
	"time"

	"hermex/internal/objectstore"
)

// Mail tips (MS-OXWSMSG GetMailTips) give a sender compose-time hints about the
// recipients they are about to email. hermEX v1 serves the out-of-office tip: when
// a recipient resolves to a local mailbox whose auto-reply is active, the sender
// sees that recipient's reply before sending. The OOF message is disclosed to the
// sender by design (this is the whole point of the tip); only the internal reply
// is shown, since the caller is an authenticated organization sender.
//
// The other tip kinds (mailbox-full, custom tip, distribution-list counts,
// moderation) are not modelled, so the MailTipsRequested filter is not honored:
// every recipient's response simply carries the tips hermEX can compute.

// --- request wire types ---

// mailTipsMailbox is a request mailbox; only the address is load-bearing.
type mailTipsMailbox struct {
	EmailAddress string `xml:"EmailAddress"`
}

type getMailTipsRequest struct {
	Recipients struct {
		Mailbox []mailTipsMailbox `xml:"Mailbox"`
	} `xml:"Recipients"`
}

// --- response wire types ---

type getMailTipsResponse struct {
	XMLName          xml.Name                 `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetMailTipsResponse"`
	ResponseClass    string                   `xml:"ResponseClass,attr"`
	ResponseCode     string                   `xml:"ResponseCode"`
	ResponseMessages mailTipsResponseMessages `xml:"ResponseMessages"`
}

type mailTipsResponseMessages struct {
	Items []mailTipsResponseMessage `xml:"MailTipsResponseMessageType"`
}

type mailTipsResponseMessage struct {
	ResponseClass string   `xml:"ResponseClass,attr"`
	ResponseCode  string   `xml:"ResponseCode"`
	MailTips      mailTips `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MailTips"`
}

// mailTips carries one recipient's tips. The element is in the messages namespace
// but its children are in the types namespace (per MS-OXWSMSG).
type mailTips struct {
	RecipientAddress mailTipRecipient `xml:"http://schemas.microsoft.com/exchange/services/2006/types RecipientAddress"`
	OutOfOffice      *oofMailTip      `xml:"http://schemas.microsoft.com/exchange/services/2006/types OutOfOffice,omitempty"`
	MailboxFull      bool             `xml:"http://schemas.microsoft.com/exchange/services/2006/types MailboxFull"`
}

type mailTipRecipient struct {
	EmailAddress string `xml:"EmailAddress"`
	RoutingType  string `xml:"RoutingType"`
}

type oofMailTip struct {
	ReplyBody mailTipReplyBody `xml:"ReplyBody"`
}

type mailTipReplyBody struct {
	Message string `xml:"Message"`
}

// --- handler ---

// handleGetMailTips answers GetMailTips: one response message per requested
// recipient, carrying the out-of-office tip when that recipient is a local mailbox
// with an active auto-reply.
func (s *Server) handleGetMailTips(w http.ResponseWriter, inner []byte, _ *session) {
	var req getMailTipsRequest
	_ = xml.Unmarshal(inner, &req)

	now := time.Now().Unix()
	msgs := make([]mailTipsResponseMessage, 0, len(req.Recipients.Mailbox))
	for _, rcpt := range req.Recipients.Mailbox {
		msgs = append(msgs, s.mailTipFor(rcpt.EmailAddress, now))
	}

	writeResponse(w, getMailTipsResponse{
		ResponseClass:    "Success",
		ResponseCode:     "NoError",
		ResponseMessages: mailTipsResponseMessages{Items: msgs},
	})
}

// mailTipFor builds one recipient's MailTips response message.
func (s *Server) mailTipFor(address string, now int64) mailTipsResponseMessage {
	tips := mailTips{
		RecipientAddress: mailTipRecipient{EmailAddress: address, RoutingType: "SMTP"},
		MailboxFull:      false,
	}
	if reply := s.recipientActiveOOF(address, now); reply != "" {
		tips.OutOfOffice = &oofMailTip{ReplyBody: mailTipReplyBody{Message: reply}}
	}
	return mailTipsResponseMessage{ResponseClass: "Success", ResponseCode: "NoError", MailTips: tips}
}

// recipientActiveOOF returns a recipient's active internal out-of-office reply, or
// "" when the address is not a local mailbox or its auto-reply is not active. A
// non-local address (a valid external recipient is indistinguishable from a typo)
// yields no tip rather than a false "invalid recipient".
func (s *Server) recipientActiveOOF(address string, now int64) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	path, ok := s.accounts.Resolve(address)
	if !ok {
		return ""
	}
	st, err := objectstore.Open(path)
	if err != nil {
		return ""
	}
	defer st.Close()
	cfg, err := st.GetOOFSettings()
	if err != nil || !cfg.OOFActive(now) {
		return ""
	}
	return cfg.InternalReply
}
