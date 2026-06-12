package mapi

// Named-property kinds (MS-OXCDATA §2.6.1). They select how a named property is
// identified within its GUID namespace.
const (
	// MnidID identifies a named property by a 32-bit long id (LID).
	MnidID uint8 = 0
	// MnidString identifies a named property by a Unicode name string.
	MnidString uint8 = 1
	// KindNone marks an unresolved name (no id and no string follow the GUID).
	KindNone uint8 = 0xFF
)

// PropertyName is a named property (MS-OXCDATA §2.6.1 PropertyName): a GUID
// namespace plus either a long id (Kind == MnidID) or a name string (Kind ==
// MnidString). For KindNone neither LID nor Name is meaningful.
type PropertyName struct {
	Kind uint8
	GUID GUID
	LID  uint32
	Name string
}
