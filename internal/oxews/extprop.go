package oxews

import (
	"errors"
	"fmt"
	"strconv"

	"hermex/internal/mapi"
)

// ErrInvalidExtendedField is a PathToExtendedFieldType a client sent that names no
// property this server can address ([MS-OXWSXPROP] 2.2.4.2 restricts which
// attribute combinations are valid).
var ErrInvalidExtendedField = errors.New("oxews: invalid extended field")

// ExtendedFieldURI is the EWS <t:ExtendedFieldURI> element: one MAPI property named
// either by its tag or by a property set and a long id or a name.
type ExtendedFieldURI struct {
	DistinguishedPropertySetID string `xml:"DistinguishedPropertySetId,attr,omitempty"`
	PropertySetID              string `xml:"PropertySetId,attr,omitempty"`
	PropertyTag                string `xml:"PropertyTag,attr,omitempty"`
	PropertyName               string `xml:"PropertyName,attr,omitempty"`
	PropertyID                 string `xml:"PropertyId,attr,omitempty"`
	PropertyType               string `xml:"PropertyType,attr"`
}

// ExtendedProperty is the EWS <t:ExtendedProperty> element: a field URI and its
// value, or its values for an array type.
type ExtendedProperty struct {
	FieldURI ExtendedFieldURI `xml:"ExtendedFieldURI"`
	Value    *string          `xml:"Value,omitempty"`
	Values   *ExtendedValues  `xml:"Values,omitempty"`
}

// ExtendedValues is the <t:Values> list of an array-typed extended property.
type ExtendedValues struct {
	Value []string `xml:"Value"`
}

// distinguishedSets are the DistinguishedPropertySetId names ([MS-OXWSCDATA]
// 2.2.5.10) and the property sets they stand for.
var distinguishedSets = map[string]mapi.GUID{
	"Meeting":           mapi.PsetidMeeting,
	"Appointment":       mapi.PsetidAppointment,
	"Common":            mapi.PsetidCommon,
	"PublicStrings":     mapi.PsPublicStrings,
	"Address":           mapi.PsetidAddress,
	"InternetHeaders":   mapi.PsInternetHeaders,
	"CalendarAssistant": {Data1: 0x11000E07, Data2: 0xB51B, Data3: 0x40D6, Data4: [8]byte{0xAF, 0x21, 0xCA, 0xA8, 0x5E, 0xDA, 0xB1, 0xD0}},
	"UnifiedMessaging":  {Data1: 0x4442858E, Data2: 0xA9E3, Data3: 0x4E80, Data4: [8]byte{0xB9, 0x00, 0x31, 0x7A, 0x21, 0x0C, 0xC1, 0x5B}},
	"Task":              mapi.PsetidTask,
	"Sharing":           {Data1: 0x00062040, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}},
}

// propertyTypes are the MapiPropertyTypeType names ([MS-OXWSXPROP] 2.2.5.1) this
// server reads and writes, and the MAPI types they stand for.
var propertyTypes = map[string]mapi.PropType{
	"ApplicationTime": mapi.PtAppTime, "ApplicationTimeArray": mapi.PtMvAppTime,
	"Binary": mapi.PtBinary, "BinaryArray": mapi.PtMvBinary,
	"Boolean": mapi.PtBoolean,
	"CLSID":   mapi.PtCLSID, "CLSIDArray": mapi.PtMvCLSID,
	"Currency": mapi.PtCurrency, "CurrencyArray": mapi.PtMvCurrency,
	"Double": mapi.PtDouble, "DoubleArray": mapi.PtMvDouble,
	"Error": mapi.PtError,
	"Float": mapi.PtFloat, "FloatArray": mapi.PtMvFloat,
	"Integer": mapi.PtLong, "IntegerArray": mapi.PtMvLong,
	"Long": mapi.PtI8, "LongArray": mapi.PtMvI8,
	"Short": mapi.PtShort, "ShortArray": mapi.PtMvShort,
	"SystemTime": mapi.PtSysTime, "SystemTimeArray": mapi.PtMvSysTime,
	"String": mapi.PtUnicode, "StringArray": mapi.PtMvUnicode,
}

// FieldTarget is the property a field URI addresses: a tag for a tagged property,
// or a name to resolve in the mailbox for a named one, and the type in both cases.
type FieldTarget struct {
	Type  mapi.PropType
	Named bool
	ID    uint16            // the property id of a tagged property
	Name  mapi.PropertyName // the name of a named property
}

// Target reads what u addresses. It refuses a URI whose type is unknown, that names
// both a tag and a set, a set without an id or a name, or a tagged id in the named
// range.
func (u ExtendedFieldURI) Target() (FieldTarget, error) {
	t, ok := propertyTypes[u.PropertyType]
	if !ok {
		return FieldTarget{}, fmt.Errorf("%w: property type %q", ErrInvalidExtendedField, u.PropertyType)
	}
	if u.PropertyTag != "" {
		return u.taggedTarget(t)
	}
	name, err := u.propertyName()
	if err != nil {
		return FieldTarget{}, err
	}
	return FieldTarget{Type: t, Named: true, Name: name}, nil
}

// taggedTarget reads a PropertyTag URI, which names no property set.
func (u ExtendedFieldURI) taggedTarget(t mapi.PropType) (FieldTarget, error) {
	if u.DistinguishedPropertySetID != "" || u.PropertySetID != "" || u.PropertyName != "" || u.PropertyID != "" {
		return FieldTarget{}, fmt.Errorf("%w: a property tag with a property set", ErrInvalidExtendedField)
	}
	id, err := strconv.ParseUint(u.PropertyTag, 0, 16)
	if err != nil || id == 0 || id >= 0x8000 {
		return FieldTarget{}, fmt.Errorf("%w: property tag %q", ErrInvalidExtendedField, u.PropertyTag)
	}
	return FieldTarget{Type: t, ID: uint16(id)}, nil
}

// propertyName reads the set and the long id or name of a named-property URI.
func (u ExtendedFieldURI) propertyName() (mapi.PropertyName, error) {
	set, err := u.propertySet()
	if err != nil {
		return mapi.PropertyName{}, err
	}
	switch {
	case u.PropertyName != "" && u.PropertyID == "":
		return mapi.PropertyName{Kind: mapi.MnidString, GUID: set, Name: u.PropertyName}, nil
	case u.PropertyID != "" && u.PropertyName == "":
		lid, err := strconv.ParseUint(u.PropertyID, 0, 32)
		if err != nil {
			return mapi.PropertyName{}, fmt.Errorf("%w: property id %q", ErrInvalidExtendedField, u.PropertyID)
		}
		return mapi.PropertyName{Kind: mapi.MnidID, GUID: set, LID: uint32(lid)}, nil
	}
	return mapi.PropertyName{}, fmt.Errorf("%w: want exactly one of a property id and a name", ErrInvalidExtendedField)
}

// propertySet reads the one property set a named-property URI gives.
func (u ExtendedFieldURI) propertySet() (mapi.GUID, error) {
	switch {
	case u.DistinguishedPropertySetID != "" && u.PropertySetID == "":
		if g, ok := distinguishedSets[u.DistinguishedPropertySetID]; ok {
			return g, nil
		}
	case u.PropertySetID != "" && u.DistinguishedPropertySetID == "":
		if g, err := mapi.ParseGUID(u.PropertySetID); err == nil {
			return g, nil
		}
	}
	return mapi.GUID{}, fmt.Errorf("%w: property set", ErrInvalidExtendedField)
}

// Echo is the field URI a response names a property by: the one the client asked
// for, with the tag in its canonical hexadecimal form. A distinguished set and an
// explicit set GUID are never written together; the schema gives a URI one or the
// other.
func (u ExtendedFieldURI) Echo() ExtendedFieldURI {
	out := u
	if out.DistinguishedPropertySetID != "" {
		out.PropertySetID = ""
	}
	if id, err := strconv.ParseUint(u.PropertyTag, 0, 16); err == nil && u.PropertyTag != "" {
		out.PropertyTag = fmt.Sprintf("0x%x", id)
	}
	return out
}

// Key is a string that equals another URI's key exactly when both address the
// same property, whichever of the equivalent spellings each used.
func (t FieldTarget) Key() string {
	if !t.Named {
		return fmt.Sprintf("tag:%04x:%04x", t.ID, uint16(t.Type))
	}
	if t.Name.Kind == mapi.MnidString {
		return fmt.Sprintf("name:%s:%s:%04x", t.Name.GUID, t.Name.Name, uint16(t.Type))
	}
	return fmt.Sprintf("lid:%s:%x:%04x", t.Name.GUID, t.Name.LID, uint16(t.Type))
}
