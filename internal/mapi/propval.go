package mapi

// TaggedPropVal is a single property value labelled with its PropTag
// (MS-OXCDATA TaggedPropertyValue). The value is not self-typed on the wire;
// its type is PropTag.Type(). Package ext encodes and decodes it.
//
// The Go type carried in Value is fixed per property type:
//
//	PtShort               int16
//	PtLong                int32
//	PtError               uint32
//	PtFloat               float32
//	PtDouble, PtAppTime   float64
//	PtCurrency, PtI8      int64
//	PtSysTime             uint64   (FILETIME, 100ns ticks since 1601)
//	PtBoolean             bool
//	PtString8, PtUnicode  string
//	PtCLSID               GUID
//	PtBinary              []byte
//	PtNull                nil
//	PtMv* (multivalue)    a slice of the corresponding scalar Go type
type TaggedPropVal struct {
	Tag   PropTag
	Value any
}

// PropertyValues is an ordered set of tagged property values
// (MS-OXCDATA PropertyValueArray / Gromox TPROPVAL_ARRAY).
type PropertyValues []TaggedPropVal
