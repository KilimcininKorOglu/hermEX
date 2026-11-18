package sendas

import (
	"bytes"
	"net/mail"
)

// FromClaim says what identity a client-built message's From header claims.
type FromClaim int

const (
	// FromAbsent is a message with no From field: it claims no identity.
	FromAbsent FromClaim = iota
	// FromOne is a message with exactly one From field naming exactly one address.
	FromOne
	// FromInvalid is every other message: one that does not parse, a From field
	// that does not parse, a field naming several addresses, or several fields.
	// Each can carry an address the gate never sees, so a caller must refuse it.
	FromInvalid
)

// HeaderFrom reads the From identity a client-built message asserts, for the
// gate to authorize. RFC 5322 3.6 allows one From field and 3.6.2 a list in it,
// but a compose never emits either form, and both let a message name an address
// beside the one a gate checks, so only a single field with a single address is
// FromOne. A caller decides for itself what FromAbsent means on its surface.
func HeaderFrom(raw []byte) (string, FromClaim) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", FromInvalid
	}
	fields := msg.Header["From"]
	switch len(fields) {
	case 0:
		return "", FromAbsent
	case 1:
	default:
		return "", FromInvalid
	}
	list, err := mail.ParseAddressList(fields[0])
	if err != nil || len(list) != 1 {
		return "", FromInvalid
	}
	return list[0].Address, FromOne
}
