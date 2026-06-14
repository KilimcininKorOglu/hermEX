package oxews

import (
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// OutgoingInput is the data CreateItem extracts from a <t:Message> to build an
// outgoing IPM.Note. Bcc is intentionally absent: the delivery message carries
// only To/Cc recipient bags so Bcc never appears on the wire; the handler
// delivers to the Bcc addresses separately.
type OutgoingInput struct {
	From      string
	Subject   string
	Body      string
	BodyType  string // "Text" or "HTML"
	To        []Mailbox
	Cc        []Mailbox
	MessageID string
	Sent      time.Time
}

// BuildOutgoing builds an outgoing IPM.Note for oxcmail.Export, mirroring the
// webmail compose path so delivery flows through Export (never hand-rolled MIME).
// Message-ID and submit time are set explicitly — Export has no fallback, so a
// miss would ship dateless/idless mail.
func BuildOutgoing(in OutgoingInput) *oxcmail.Message {
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, "IPM.Note")
	if in.From != "" {
		props.Set(mapi.PrSentRepresentingSmtpAddress, in.From)
		props.Set(mapi.PrSentRepresentingEmailAddress, in.From)
		props.Set(mapi.PrSentRepresentingAddrType, "SMTP")
	}
	props.Set(mapi.PrSubject, in.Subject)
	props.Set(mapi.PrClientSubmitTime, mapi.UnixToNTTime(in.Sent))
	props.Set(mapi.PrInternetMessageID, in.MessageID)
	if strings.EqualFold(in.BodyType, "HTML") {
		props.Set(mapi.PrHTML, []byte(toCRLF(in.Body)))
	} else {
		props.Set(mapi.PrBody, toCRLF(in.Body))
	}
	msg := &oxcmail.Message{Props: props}
	msg.Recipients = append(msg.Recipients, outgoingBags(in.To, mapi.RecipTo)...)
	msg.Recipients = append(msg.Recipients, outgoingBags(in.Cc, mapi.RecipCc)...)
	return msg
}

// outgoingBags builds recipient property bags from mailboxes.
func outgoingBags(boxes []Mailbox, rcptType int32) []mapi.PropertyValues {
	var out []mapi.PropertyValues
	for _, b := range boxes {
		if b.EmailAddress == "" {
			continue
		}
		var bag mapi.PropertyValues
		bag.Set(mapi.PrRecipientType, rcptType)
		bag.Set(mapi.PrAddrType, "SMTP")
		bag.Set(mapi.PrEmailAddress, b.EmailAddress)
		bag.Set(mapi.PrSmtpAddress, b.EmailAddress)
		if b.Name != "" {
			bag.Set(mapi.PrDisplayName, b.Name)
		}
		out = append(out, bag)
	}
	return out
}

// toCRLF normalizes line endings to CRLF for the wire/store.
func toCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
