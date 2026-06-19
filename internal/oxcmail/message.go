package oxcmail

import "hermex/internal/mapi"

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

// BodyFormat selects which body representations Export emits.
type BodyFormat int

const (
	// BodyPlainAndHTML emits text/plain and text/html as the message carries
	// them (multipart/alternative when both are present).
	BodyPlainAndHTML BodyFormat = iota
	// BodyPlainOnly emits only a text/plain body.
	BodyPlainOnly
	// BodyHTMLOnly emits only a text/html body.
	BodyHTMLOnly
)

// Options configures a conversion. Resolver supplies named-property ids and is
// required whenever the message carries named properties. BodyFormat selects
// the body representation Export emits (the zero value emits plain and HTML).
//
// CalendarBody, when set, is a pre-rendered iCalendar object (an iTIP message
// the caller built through oxcical, which oxcmail cannot import without a cycle)
// that Export carries as a text/calendar alternative beside the text body;
// CalendarMethod is its METHOD, surfaced on the part's Content-Type.
//
// CalendarImporter is the import-side counterpart: it parses a text/calendar part
// the caller's iCalendar converter understands, letting Import overlay a scheduling
// message's class and appointment properties (see CalendarImporter).
type Options struct {
	Resolver         PropIDResolver
	BodyFormat       BodyFormat
	CalendarBody     []byte
	CalendarMethod   string
	CalendarImporter CalendarImporter
}

// CalendarImporter parses a text/calendar body (a UTF-8 iCalendar object) into the
// MAPI properties of the scheduling object it describes. The caller supplies it to
// bridge to the iCalendar converter that oxcmail cannot import directly (it would
// form an import cycle); a nil importer leaves calendar parts unparsed (carried as
// attachments). It returns an error when the body is not a parseable calendar.
type CalendarImporter func(ical []byte) (mapi.PropertyValues, error)
