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
type Options struct {
	Resolver   PropIDResolver
	BodyFormat BodyFormat
}
