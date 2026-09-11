package oxews

import (
	"encoding/xml"
	"time"

	"hermex/internal/oxcmail"
)

// The MeetingRequestType values ([MS-OXWSMTGS] MeetingRequestTypeType) this server
// emits. A client reads the element to tell an actionable invitation from an
// ordinary message: New is a first invitation, FullUpdate a revision of one.
const (
	MeetingTypeNew        = "NewMeetingRequest"
	MeetingTypeFullUpdate = "FullUpdate"
)

// MeetingRequestClass is the stored message class a delivered invitation carries.
const MeetingRequestClass = "IPM.Schedule.Meeting.Request"

// MeetingRequest is the EWS <t:MeetingRequest> element (the
// MeetingRequestMessageType subset this server emits). The field order IS the wire
// order: MeetingRequestMessageType is an XSD sequence, so a client that validates
// the response rejects a reordered element. The order below follows the documented
// sequence, mail fields first and the meeting fields after ReplyTo.
type MeetingRequest struct {
	XMLName             xml.Name        `xml:"http://schemas.microsoft.com/exchange/services/2006/types MeetingRequest"`
	ItemID              ItemIDElem      `xml:"ItemId"`
	ItemClass           string          `xml:"ItemClass,omitempty"`
	Subject             string          `xml:"Subject,omitempty"`
	Sensitivity         string          `xml:"Sensitivity,omitempty"`
	Body                *Body           `xml:"Body,omitempty"`
	Attachments         *AttachmentList `xml:"Attachments,omitempty"`
	DateTimeReceived    string          `xml:"DateTimeReceived,omitempty"`
	Size                int             `xml:"Size,omitempty"`
	Importance          string          `xml:"Importance,omitempty"`
	DateTimeSent        string          `xml:"DateTimeSent,omitempty"`
	HasAttachments      bool            `xml:"HasAttachments"`
	Sender              *Recipient      `xml:"Sender,omitempty"`
	ToRecipients        *RecipientList  `xml:"ToRecipients,omitempty"`
	CcRecipients        *RecipientList  `xml:"CcRecipients,omitempty"`
	From                *Recipient      `xml:"From,omitempty"`
	IsRead              bool            `xml:"IsRead"`
	IsResponseRequested bool            `xml:"IsResponseRequested"`
	// The meeting fields. MeetingRequestType is the one a client keys on to decide
	// whether to offer Accept / Tentative / Decline.
	MeetingRequestType     string     `xml:"MeetingRequestType,omitempty"`
	IntendedFreeBusyStatus string     `xml:"IntendedFreeBusyStatus,omitempty"`
	Start                  string     `xml:"Start,omitempty"`
	End                    string     `xml:"End,omitempty"`
	IsAllDayEvent          bool       `xml:"IsAllDayEvent"`
	LegacyFreeBusyStatus   string     `xml:"LegacyFreeBusyStatus,omitempty"`
	Location               string     `xml:"Location,omitempty"`
	IsMeeting              bool       `xml:"IsMeeting"`
	Organizer              *Recipient `xml:"Organizer,omitempty"`
	UID                    string     `xml:"UID,omitempty"`
}

// MeetingMeta carries the appointment facts a meeting request is rendered from.
// The caller resolves them from the store's named properties, so this package stays
// free of store and named-property knowledge, exactly as BuildItem does.
type MeetingMeta struct {
	Start             time.Time
	End               time.Time
	AllDay            bool
	Location          string
	UID               string
	BusyStatus        string // LegacyFreeBusyType name (Free/Tentative/Busy/OOF/WorkingElsewhere)
	ResponseRequested bool
	RequestType       string // a MeetingType* value
	Organizer         *Mailbox
}

// BuildMeetingRequest renders a delivered invitation as <t:MeetingRequest>. The mail
// half is built exactly as BuildItem builds a <t:Message>, so a client reading one
// element or the other sees the same subject, body, attachments and recipients; the
// meeting half comes from mr.
func BuildMeetingRequest(msg *oxcmail.Message, meta ItemMeta, mr MeetingMeta) MeetingRequest {
	m := BuildItem(msg, meta)
	out := MeetingRequest{
		ItemID:              m.ItemID,
		ItemClass:           m.ItemClass,
		Subject:             m.Subject,
		Sensitivity:         m.Sensitivity,
		Body:                m.Body,
		Attachments:         m.Attachments,
		DateTimeReceived:    m.DateTimeReceived,
		Size:                m.Size,
		Importance:          m.Importance,
		DateTimeSent:        m.DateTimeSent,
		HasAttachments:      m.HasAttachments,
		Sender:              m.Sender,
		ToRecipients:        m.ToRecipients,
		CcRecipients:        m.CcRecipients,
		From:                m.From,
		IsRead:              m.IsRead,
		IsResponseRequested: mr.ResponseRequested,

		MeetingRequestType:     mr.RequestType,
		IntendedFreeBusyStatus: mr.BusyStatus,
		LegacyFreeBusyStatus:   mr.BusyStatus,
		IsAllDayEvent:          mr.AllDay,
		Location:               mr.Location,
		IsMeeting:              true,
		UID:                    mr.UID,
	}
	if !mr.Start.IsZero() {
		out.Start = mr.Start.UTC().Format(ewsTime)
	}
	if !mr.End.IsZero() {
		out.End = mr.End.UTC().Format(ewsTime)
	}
	if mr.Organizer != nil {
		out.Organizer = &Recipient{Mailbox: *mr.Organizer}
	}
	return out
}
