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
// (MS-OXCDATA PropertyValueArray).
type PropertyValues []TaggedPropVal

// Get returns the value stored for tag and whether it is present.
func (pv PropertyValues) Get(tag PropTag) (any, bool) {
	for i := range pv {
		if pv[i].Tag == tag {
			return pv[i].Value, true
		}
	}
	return nil, false
}

// Has reports whether a value is stored for tag.
func (pv PropertyValues) Has(tag PropTag) bool {
	_, ok := pv.Get(tag)
	return ok
}

// Set stores val for tag, replacing any existing value for that tag. A property
// set is keyed by tag (MS-OXCDATA), so setting an existing tag overwrites rather
// than duplicates; a new tag is appended, preserving insertion order.
func (pv *PropertyValues) Set(tag PropTag, val any) {
	for i := range *pv {
		if (*pv)[i].Tag == tag {
			(*pv)[i].Value = val
			return
		}
	}
	*pv = append(*pv, TaggedPropVal{Tag: tag, Value: val})
}
