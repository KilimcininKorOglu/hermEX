package oxcmail

import (
	"time"

	"hermex/internal/mapi"
)

// Message is a MAPI message object: the top-level property bag, the recipient
// table (one property bag per recipient), and the attachment table. Import
// produces one; Export consumes one.
type Message struct {
	Props       mapi.PropertyValues
	Recipients  []mapi.PropertyValues
	Attachments []Attachment
}

// Attachment is one attachment, carried as a property bag: the data, filename,
// MIME type, content id, method, and flags all live as properties.
type Attachment struct {
	Props mapi.PropertyValues
}

// PropIDResolver resolves named properties to store property ids. With create
// true (used during Import), names not yet known are allocated; the result is
// parallel to names, with 0 for an unresolved name. It is satisfied by the
// store's named-property allocator.
type PropIDResolver func(create bool, names []mapi.PropertyName) ([]uint16, error)

// ContactCard renders the contact properties an attachment keeps in
// PrEmbeddedContact as a vCard 3.0, returning the card and the contact's display
// name. It is satisfied by oxvcard.EmbeddedCard, which oxcmail cannot import.
type ContactCard func(blob []byte) (card []byte, name string, err error)

// ContactFromCard is the import-side counterpart of ContactCard: it converts a
// received vCard into the encoded contact PrEmbeddedContact keeps, with the
// contact's display name. It is satisfied by oxvcard.CardContact.
type ContactFromCard func(card []byte) (blob []byte, name string, err error)

// ForeignResolver resolves named properties a message's sender chose, allocating
// a new name only within the store's quota; the result is parallel to names, 0
// for a name left unresolved. It is satisfied by the store's
// GetForeignNamedPropIDs.
type ForeignResolver func(names []mapi.PropertyName) ([]uint16, error)

// PropNameResolver resolves a store property id back to its named property, the
// reverse of PropIDResolver; ok is false for an id with no name. It is satisfied
// by the store's NamedPropName.
type PropNameResolver func(propid uint16) (name mapi.PropertyName, ok bool, err error)

// Options configures a conversion. Resolver supplies named-property ids and is
// required whenever the message carries named properties.
//
// Export emits the body representations the message itself carries: text/plain,
// text/html, or a multipart/alternative of both. There is no option to select
// one, because there is no caller that needs one; a selector was declared here
// once and never wired into the rendering path, which promised behavior Export
// did not have.
//
// CalendarBody, when set, is a pre-rendered iCalendar object (an iTIP message
// the caller built through oxcical, which oxcmail cannot import without a cycle)
// that Export carries as a text/calendar alternative beside the text body;
// CalendarMethod is its METHOD, surfaced on the part's Content-Type.
//
// CalendarImporter is the import-side counterpart: it parses a text/calendar part
// the caller's iCalendar converter understands, letting Import overlay a scheduling
// message's class and appointment properties (see CalendarImporter).
//
// ContactCard lets Export send an attached contact as the vCard [MS-OXCMAIL]
// 2.1.3.4.6 calls for; without it the contact goes as the message it is stored as.
// ContactFromCard lets Import store a received vCard as an attached contact
// ([MS-OXCMAIL] 2.2.3.4.4); without it the card stays a file attachment.
//
// ForeignResolver maps the named properties a sender chose, the ones a TNEF part
// carries, under the store's quota for such names; without it those properties
// are dropped.
//
// PropName lets Export read the header fields a message stores as
// PS_INTERNET_HEADERS named properties; without it none are written.
//
// ArrivalTime is when the message reached the store. Import uses it as the submit
// time of a message without a Date header, as the receiving MTA adds one from the
// time it received the message (RFC 5321 section 6.4); a zero value falls back to
// the current time.
//
// TNEF makes Export write the form a recipient that accepts rich information reads:
// the plain text body and a winmail.dat that carries the properties and the
// attachments. PropName names the named properties the stream carries.
type Options struct {
	Resolver         PropIDResolver
	PropName         PropNameResolver
	ForeignResolver  ForeignResolver
	ContactCard      ContactCard
	ContactFromCard  ContactFromCard
	CalendarBody     []byte
	CalendarMethod   string
	CalendarImporter CalendarImporter
	ArrivalTime      time.Time
	TNEF             bool
}

// CalendarImporter parses a text/calendar body (a UTF-8 iCalendar object) into the
// MAPI properties of the scheduling object it describes. The caller supplies it to
// bridge to the iCalendar converter that oxcmail cannot import directly (it would
// form an import cycle); a nil importer leaves calendar parts unparsed (carried as
// attachments). It returns an error when the body is not a parseable calendar.
type CalendarImporter func(ical []byte) (mapi.PropertyValues, error)
