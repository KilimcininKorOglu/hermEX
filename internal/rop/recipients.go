package rop

import (
	"strings"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// writeRecipientTable emits the inline recipient table shared by the RopOpenMessage
// and RopReloadCachedInformation responses ([MS-OXCMSG] 2.2.3.1.2 / 2.2.3.6):
// RecipientCount, an (empty) RecipientColumns array, RowCount, then one
// OPENRECIPIENT_ROW per recipient. The essential recipient fields travel in each
// row's flag-driven section, so no extra columns are projected. RowCount is a single
// byte; any recipients beyond 255 are not inlined (a client reads them with
// RopReadRecipients, which is out of scope here).
func writeRecipientTable(out *ext.Push, recipients []mapi.PropertyValues) {
	out.Uint16(uint16(len(recipients))) // RecipientCount (total, not just inlined)
	_ = out.PropTags(nil)               // RecipientColumns (empty)
	n := min(len(recipients), 0xFF)
	out.Uint8(uint8(n)) // RowCount
	for _, r := range recipients[:n] {
		writeOpenRecipientRow(out, r)
	}
}

// writeOpenRecipientRow emits one OPENRECIPIENT_ROW: the recipient type, a code page
// id and reserved field, then the RECIPIENT_ROW body.
func writeOpenRecipientRow(out *ext.Push, r mapi.PropertyValues) {
	rcptType := mapi.RecipTo
	if v, ok := r.Get(mapi.PrRecipientType); ok {
		if n, ok := v.(int32); ok {
			rcptType = int(n)
		}
	}
	out.Uint8(uint8(rcptType)) // RecipientType (1 To, 2 Cc, 3 Bcc)
	out.Uint16(0)              // CodePageId (Unicode rows carry their own encoding)
	out.Uint16(0)              // Reserved
	pushRecipientRow(out, r)
}

// pushRecipientRow encodes a RECIPIENT_ROW ([MS-OXCDATA] 2.8.3.2) for a stored
// recipient. It is the inverse of pullRecipientRow: Unicode display and email,
// the SMTP/EX/other address kind derived from PR_ADDRTYPE, and an empty trailing
// PROPERTY_ROW.
func pushRecipientRow(out *ext.Push, r mapi.PropertyValues) {
	display := stringProp(r, mapi.PrDisplayName)
	addrType := stringProp(r, mapi.PrAddrType)
	email := stringProp(r, mapi.PrEmailAddress)
	if smtp := stringProp(r, mapi.PrSmtpAddress); smtp != "" && (addrType == "" || strings.EqualFold(addrType, "SMTP")) {
		email, addrType = smtp, "SMTP"
	}

	flags := recipientRowUnicode
	if display != "" {
		flags |= recipientRowDisplay
	}
	if boolProp(r, mapi.PrResponsibility) {
		flags |= recipientRowResponsible
	}
	if boolProp(r, mapi.PrSendRichInfo) {
		flags |= recipientRowNonRich
	}

	switch {
	case strings.EqualFold(addrType, "EX"):
		flags |= addrKindX500DN
	case addrType == "" || strings.EqualFold(addrType, "SMTP"):
		flags |= addrKindSMTP
		if email != "" {
			flags |= recipientRowEmail
		}
	default:
		flags |= addrKindNoType | recipientRowOutOfStandard
		if email != "" {
			flags |= recipientRowEmail
		}
	}
	out.Uint16(flags)

	switch flags & 0x0007 {
	case addrKindX500DN:
		out.Uint8(0)       // PrefixUsed
		out.Uint8(0)       // DisplayType
		out.String8(email) // X500DN (the EX address; always ASCII)
	case addrKindNoType:
		if flags&recipientRowOutOfStandard != 0 {
			out.String8(addrType) // AddressType (always ASCII)
		}
	}
	if flags&recipientRowEmail != 0 {
		out.Unicode(email)
	}
	if flags&recipientRowDisplay != 0 {
		out.Unicode(display)
	}
	out.Uint16(0)                     // RecipientColumnCount (no extra columns)
	_ = buildPropertyRow(out, nil, r) // empty PROPERTY_ROW (single NONE flag byte)
}

// boolProp reads a PtBoolean property, defaulting to false when absent.
func boolProp(pv mapi.PropertyValues, tag mapi.PropTag) bool {
	if v, ok := pv.Get(tag); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// messageRecipientBags returns a message object's recipient property bags, covering
// a stored message, an embedded message, and an in-progress compose. It mirrors
// messageAttachmentBags.
func messageRecipientBags(o *object) ([]mapi.PropertyValues, error) {
	switch {
	case o.kind == kindEmbedded:
		if o.embedded == nil || o.embedded.msg == nil {
			return nil, nil
		}
		return o.embedded.msg.Recipients, nil
	case o.kind == kindNewMessage:
		if o.newMsg == nil {
			return nil, nil
		}
		return o.newMsg.recipients, nil
	case o.store != nil && o.messageID != 0:
		msg, err := o.store.OpenMessage(o.messageID)
		if err != nil {
			return nil, err
		}
		return msg.Recipients, nil
	default:
		return nil, nil
	}
}
