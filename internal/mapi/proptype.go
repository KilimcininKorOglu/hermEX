package mapi

import "fmt"

// PropType is a 16-bit MAPI property type. The values are fixed by the
// external Exchange protocols (MS-OXCDATA §2.11.1) and identify how a property
// value is encoded on the wire; see package ext for the encoding itself.
type PropType uint16

// Property types. Values are the canonical MAPI/MS-OXCDATA type tags.
const (
	PtUnspecified PropType = 0x0000 // PtypUnspecified
	PtNull        PropType = 0x0001 // PtypNull
	PtShort       PropType = 0x0002 // PtypInteger16 (signed)
	PtLong        PropType = 0x0003 // PtypInteger32 (signed)
	PtFloat       PropType = 0x0004 // PtypFloating32
	PtDouble      PropType = 0x0005 // PtypFloating64
	PtCurrency    PropType = 0x0006 // PtypCurrency (int64, 1/10000 unit)
	PtAppTime     PropType = 0x0007 // PtypFloatingTime
	PtError       PropType = 0x000A // PtypErrorCode (uint32)
	PtBoolean     PropType = 0x000B // PtypBoolean (1 byte on the wire)
	PtObject      PropType = 0x000D // PtypObject / PtypEmbeddedTable
	PtI8          PropType = 0x0014 // PtypInteger64 (signed)
	PtString8     PropType = 0x001E // PtypString8 (code-page string)
	PtUnicode     PropType = 0x001F // PtypString (UTF-16LE on the MS wire)
	PtSysTime     PropType = 0x0040 // PtypTime (8-byte FILETIME)
	PtCLSID       PropType = 0x0048 // PtypGuid
	PtSvrEID      PropType = 0x00FB // PtypServerId
	PtRestriction PropType = 0x00FD // PtypRestriction
	PtActions     PropType = 0x00FE // PtypRuleAction
	PtBinary      PropType = 0x0102 // PtypBinary

	PtMvShort    PropType = 0x1002 // PtypMultipleInteger16
	PtMvLong     PropType = 0x1003 // PtypMultipleInteger32
	PtMvFloat    PropType = 0x1004 // PtypMultipleFloating32
	PtMvDouble   PropType = 0x1005 // PtypMultipleFloating64
	PtMvCurrency PropType = 0x1006 // PtypMultipleCurrency
	PtMvAppTime  PropType = 0x1007 // PtypMultipleFloatingTime
	PtMvI8       PropType = 0x1014 // PtypMultipleInteger64
	PtMvString8  PropType = 0x101E // PtypMultipleString8
	PtMvUnicode  PropType = 0x101F // PtypMultipleString
	PtMvSysTime  PropType = 0x1040 // PtypMultipleTime
	PtMvCLSID    PropType = 0x1048 // PtypMultipleGuid
	PtMvBinary   PropType = 0x1102 // PtypMultipleBinary
)

// MvFlag marks a property type as multivalue (MS-OXCDATA's 0x1000 bit).
const MvFlag PropType = 0x1000

// IsMultivalue reports whether t carries the multivalue flag.
func (t PropType) IsMultivalue() bool { return t&MvFlag != 0 }

// Base returns the scalar type underlying a multivalue type, or t unchanged
// when t is already scalar (e.g. PtMvBinary.Base() == PtBinary).
func (t PropType) Base() PropType { return t &^ MvFlag }

func (t PropType) String() string {
	if name, ok := propTypeNames[t]; ok {
		return name
	}
	return fmt.Sprintf("PropType(0x%04X)", uint16(t))
}

var propTypeNames = map[PropType]string{
	PtUnspecified: "PtUnspecified", PtNull: "PtNull", PtShort: "PtShort",
	PtLong: "PtLong", PtFloat: "PtFloat", PtDouble: "PtDouble",
	PtCurrency: "PtCurrency", PtAppTime: "PtAppTime", PtError: "PtError",
	PtBoolean: "PtBoolean", PtObject: "PtObject", PtI8: "PtI8",
	PtString8: "PtString8", PtUnicode: "PtUnicode", PtSysTime: "PtSysTime",
	PtCLSID: "PtCLSID", PtSvrEID: "PtSvrEID", PtRestriction: "PtRestriction",
	PtActions: "PtActions", PtBinary: "PtBinary",
	PtMvShort: "PtMvShort", PtMvLong: "PtMvLong", PtMvFloat: "PtMvFloat",
	PtMvDouble: "PtMvDouble", PtMvCurrency: "PtMvCurrency", PtMvAppTime: "PtMvAppTime",
	PtMvI8: "PtMvI8", PtMvString8: "PtMvString8", PtMvUnicode: "PtMvUnicode",
	PtMvSysTime: "PtMvSysTime", PtMvCLSID: "PtMvCLSID", PtMvBinary: "PtMvBinary",
}
