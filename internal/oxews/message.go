package oxews

import (
	"encoding/xml"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// ewsTime is the EWS xs:dateTime format: ISO 8601 in UTC with a literal Z.
const ewsTime = "2006-01-02T15:04:05Z"

// Message is the EWS <t:Message> element (the ItemType/MessageType subset v1
// emits). It declares the types namespace once; children inherit it.
type Message struct {
	XMLName          xml.Name        `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
	ItemID           ItemIDElem      `xml:"ItemId"`
	Subject          string          `xml:"Subject,omitempty"`
	Sensitivity      string          `xml:"Sensitivity,omitempty"`
	Body             *Body           `xml:"Body,omitempty"`
	Attachments      *AttachmentList `xml:"Attachments,omitempty"`
	DateTimeReceived string          `xml:"DateTimeReceived,omitempty"`
	Size             int             `xml:"Size,omitempty"`
	Importance       string          `xml:"Importance,omitempty"`
	DateTimeSent     string          `xml:"DateTimeSent,omitempty"`
	HasAttachments   bool            `xml:"HasAttachments"`
	From             *Recipient      `xml:"From,omitempty"`
	ToRecipients     *RecipientList  `xml:"ToRecipients,omitempty"`
	CcRecipients     *RecipientList  `xml:"CcRecipients,omitempty"`
	IsRead           bool            `xml:"IsRead"`
}

// ItemIDElem is the EWS <t:ItemId> element.
type ItemIDElem struct {
	ID        string `xml:"Id,attr"`
	ChangeKey string `xml:"ChangeKey,attr,omitempty"`
}

// Body is the EWS <t:Body> element; the content is escaped chardata.
type Body struct {
	BodyType string `xml:"BodyType,attr"`
	Content  string `xml:",chardata"`
}

// Recipient is a single-mailbox recipient element (From/Sender).
type Recipient struct {
	Mailbox Mailbox `xml:"Mailbox"`
}

// RecipientList is a multi-mailbox recipient element (ToRecipients/CcRecipients).
type RecipientList struct {
	Mailboxes []Mailbox `xml:"Mailbox"`
}

// Mailbox is the EWS <t:Mailbox> element.
type Mailbox struct {
	Name         string `xml:"Name,omitempty"`
	EmailAddress string `xml:"EmailAddress,omitempty"`
}

// ItemMeta carries the per-item facts that do not come from the MAPI property
// bag: the opaque id, the index flags, the wire size, and the MIME-extracted
// body (the store keeps no HTML body property, so the body is read from the
// message's RFC822 form).
type ItemMeta struct {
	ItemID         string
	MessageID      int64
	ChangeKey      string
	IsRead         bool
	HasAttachments bool
	Received       time.Time
	Size           int
	Body           string
	BodyType       string // "Text" or "HTML"
}

// BuildItem renders a full <t:Message> from an opened MAPI message plus the
// item metadata (GetItem). Subject, importance, sensitivity, sender, and
// recipients come from the property bag; the body and flags from ItemMeta.
func BuildItem(msg *oxcmail.Message, meta ItemMeta) Message {
	m := Message{
		ItemID:         ItemIDElem{ID: meta.ItemID, ChangeKey: meta.ChangeKey},
		Subject:        stringProp(msg.Props, mapi.PrSubject),
		Sensitivity:    sensitivityName(longProp(msg.Props, mapi.PrSensitivity, 0)),
		Importance:     importanceName(longProp(msg.Props, mapi.PrImportance, 1)),
		HasAttachments: meta.HasAttachments,
		IsRead:         meta.IsRead,
		Size:           meta.Size,
	}
	if !meta.Received.IsZero() {
		m.DateTimeReceived = meta.Received.UTC().Format(ewsTime)
	}
	if t, ok := timeProp(msg.Props, mapi.PrClientSubmitTime); ok {
		m.DateTimeSent = t.UTC().Format(ewsTime)
	}
	if meta.Body != "" {
		m.Body = &Body{BodyType: meta.BodyType, Content: meta.Body}
	}
	m.Attachments = BuildAttachments(meta.MessageID, msg.Attachments)
	if mb := senderMailbox(msg.Props); mb != nil {
		m.From = &Recipient{Mailbox: *mb}
	}
	to, cc := recipientMailboxes(msg.Recipients)
	if len(to) > 0 {
		m.ToRecipients = &RecipientList{Mailboxes: to}
	}
	if len(cc) > 0 {
		m.CcRecipients = &RecipientList{Mailboxes: cc}
	}
	return m
}

// SummaryMeta carries the FindItem summary fields, projected from the folder
// index (no per-item property read).
type SummaryMeta struct {
	ItemID         string
	ChangeKey      string
	Subject        string
	SenderName     string
	SenderEmail    string
	Received       time.Time
	Size           int
	IsRead         bool
	HasAttachments bool
}

// BuildSummary renders a summary <t:Message> for FindItem from index-projected
// fields (no body, no full recipient list).
func BuildSummary(meta SummaryMeta) Message {
	m := Message{
		ItemID:         ItemIDElem{ID: meta.ItemID, ChangeKey: meta.ChangeKey},
		Subject:        meta.Subject,
		HasAttachments: meta.HasAttachments,
		IsRead:         meta.IsRead,
		Size:           meta.Size,
	}
	if !meta.Received.IsZero() {
		m.DateTimeReceived = meta.Received.UTC().Format(ewsTime)
	}
	if meta.SenderName != "" || meta.SenderEmail != "" {
		m.From = &Recipient{Mailbox: Mailbox{Name: meta.SenderName, EmailAddress: meta.SenderEmail}}
	}
	return m
}

// senderMailbox builds the From mailbox, preferring the sent-representing
// identity and falling back to the sender.
func senderMailbox(props mapi.PropertyValues) *Mailbox {
	name := stringProp(props, mapi.PrSentRepresentingName)
	addr := stringProp(props, mapi.PrSentRepresentingSmtpAddress)
	if name == "" && addr == "" {
		name = stringProp(props, mapi.PrSenderName)
		addr = stringProp(props, mapi.PrSenderSmtpAddress)
	}
	if name == "" && addr == "" {
		return nil
	}
	return &Mailbox{Name: name, EmailAddress: addr}
}

// recipientMailboxes splits the recipient bags into To and Cc mailboxes (Bcc is
// not exposed on a read item).
func recipientMailboxes(bags []mapi.PropertyValues) (to, cc []Mailbox) {
	for _, bag := range bags {
		mb := Mailbox{
			Name:         stringProp(bag, mapi.PrDisplayName),
			EmailAddress: recipientAddr(bag),
		}
		switch longProp(bag, mapi.PrRecipientType, mapi.RecipTo) {
		case mapi.RecipCc:
			cc = append(cc, mb)
		case mapi.RecipBcc:
			// not exposed
		default:
			to = append(to, mb)
		}
	}
	return to, cc
}

// recipientAddr returns a recipient's SMTP address, preferring PR_SMTP_ADDRESS.
func recipientAddr(bag mapi.PropertyValues) string {
	if a := stringProp(bag, mapi.PrSmtpAddress); a != "" {
		return a
	}
	return stringProp(bag, mapi.PrEmailAddress)
}

// importanceName maps PR_IMPORTANCE (0 low, 1 normal, 2 high) to the EWS value.
func importanceName(v int32) string {
	switch v {
	case 0:
		return "Low"
	case 2:
		return "High"
	default:
		return "Normal"
	}
}

// sensitivityName maps PR_SENSITIVITY (0..3) to the EWS value.
func sensitivityName(v int32) string {
	switch v {
	case 1:
		return "Personal"
	case 2:
		return "Private"
	case 3:
		return "Confidential"
	default:
		return "Normal"
	}
}

// stringProp reads a PtUnicode property as a string, or "" if absent.
func stringProp(props mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := props.Get(tag); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// longProp reads a PtLong property as an int32, or def if absent.
func longProp(props mapi.PropertyValues, tag mapi.PropTag, def int32) int32 {
	if v, ok := props.Get(tag); ok {
		switch n := v.(type) {
		case int32:
			return n
		case int64:
			return int32(n)
		case int:
			return int32(n)
		case uint32:
			return int32(n)
		}
	}
	return def
}

// timeProp reads a PtSysTime property as a time.Time.
func timeProp(props mapi.PropertyValues, tag mapi.PropTag) (time.Time, bool) {
	if v, ok := props.Get(tag); ok {
		if t, ok := v.(time.Time); ok {
			return t, true
		}
	}
	return time.Time{}, false
}
