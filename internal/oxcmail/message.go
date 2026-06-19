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
type Options struct {
	Resolver       PropIDResolver
	BodyFormat     BodyFormat
	CalendarBody   []byte
	CalendarMethod string
	// GenerateMessageID mints a Message-ID for an originating message that has no
	// PR_INTERNET_MESSAGE_ID (RFC 5322 requires one on transmitted mail). Set it
	// only on the outbound/submission path; leave it false when re-exporting a
	// stored message for serving, so a message without an id keeps the same bytes
	// on every read instead of getting a fresh id each time.
	GenerateMessageID bool
}
