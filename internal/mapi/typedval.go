package mapi

// TypedPropVal is a property value carrying its own 16-bit type on the wire
// (MS-OXCDATA §2.11.3 TypedPropertyValue). It is also the value form of a
// PtUnspecified property. The Go type in Value follows the same mapping as
// TaggedPropVal, keyed by Type.
type TypedPropVal struct {
	Type  PropType
	Value any
}

// Flagged property-value availability flags (MS-OXCDATA §2.11.5).
const (
	// FlaggedAvailable marks a value as present; Value holds the property value.
	FlaggedAvailable uint8 = 0x0
	// FlaggedUnavailable marks a value as absent; Value is nil.
	FlaggedUnavailable uint8 = 0x1
	// FlaggedError marks a value as an error; Value holds a uint32 error code.
	FlaggedError uint8 = 0xA
)

// FlaggedPropVal is a property value tagged with an availability flag
// (MS-OXCDATA §2.11.5 FlaggedPropertyValue). Value holds the property value when
// Flag is FlaggedAvailable, a uint32 error code when FlaggedError, and is nil
// when FlaggedUnavailable.
//
// Type is meaningful only for the with-type form (§2.11.6), used when the
// column's requested type is PtUnspecified: it carries the value's actual type,
// which precedes the flag on the wire. For an available value Type is the
// value's type; callers building an error value set Type to PtError. It is
// ignored for concrete-type columns.
type FlaggedPropVal struct {
	Flag  uint8
	Type  PropType
	Value any
}
