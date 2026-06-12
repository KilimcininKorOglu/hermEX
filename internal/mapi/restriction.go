package mapi

// RestrictionType identifies a restriction node (MS-OXCDATA §2.12.1 RestrictType).
type RestrictionType uint8

// Restriction node types. Values match the MAPI restriction type enumeration.
const (
	ResAnd         RestrictionType = 0x00
	ResOr          RestrictionType = 0x01
	ResNot         RestrictionType = 0x02
	ResContent     RestrictionType = 0x03
	ResProperty    RestrictionType = 0x04
	ResPropCompare RestrictionType = 0x05
	ResBitmask     RestrictionType = 0x06
	ResSize        RestrictionType = 0x07
	ResExist       RestrictionType = 0x08
	ResSub         RestrictionType = 0x09
	ResComment     RestrictionType = 0x0A
	ResCount       RestrictionType = 0x0B
	ResAnnotation  RestrictionType = 0x0C
	ResNull        RestrictionType = 0xFF
)

// Relop is a comparison operator (MS-OXCDATA §2.12.5). The values are
// contiguous 0..6 then jump to 0x64 for "member of distribution list".
type Relop uint8

const (
	RelopLT         Relop = 0
	RelopLE         Relop = 1
	RelopGT         Relop = 2
	RelopGE         Relop = 3
	RelopEQ         Relop = 4
	RelopNE         Relop = 5
	RelopRE         Relop = 6
	RelopMemberOfDL Relop = 0x64
)

// BitmaskRelop selects how a bitmask restriction tests masked bits
// (MS-OXCDATA §2.12.6): equal-to-zero or not-equal-to-zero.
type BitmaskRelop uint8

const (
	BmrEqz BitmaskRelop = 0
	BmrNez BitmaskRelop = 1
)

// Restriction is a search restriction node (MS-OXCDATA §2.12). Type selects the
// node kind; Value holds the matching payload:
//
//	ResAnd, ResOr            []Restriction
//	ResNot                   Restriction
//	ResContent               ContentRestriction
//	ResProperty              PropertyRestriction
//	ResPropCompare           ComparePropsRestriction
//	ResBitmask               BitmaskRestriction
//	ResSize                  SizeRestriction
//	ResExist                 ExistRestriction
//	ResSub                   SubRestriction
//	ResComment, ResAnnotation CommentRestriction
//	ResCount                 CountRestriction
//	ResNull                  nil
type Restriction struct {
	Type  RestrictionType
	Value any
}

// ContentRestriction matches a property value against a search value with a
// fuzzy-comparison level (MS-OXCDATA §2.12.3).
type ContentRestriction struct {
	FuzzyLevel uint32
	PropTag    PropTag
	PropVal    TaggedPropVal
}

// PropertyRestriction compares a property against a value with a relational
// operator (MS-OXCDATA §2.12.5).
type PropertyRestriction struct {
	Relop   Relop
	PropTag PropTag
	PropVal TaggedPropVal
}

// ComparePropsRestriction compares two properties (MS-OXCDATA §2.12.7).
type ComparePropsRestriction struct {
	Relop    Relop
	PropTag1 PropTag
	PropTag2 PropTag
}

// BitmaskRestriction tests masked bits of a long property (MS-OXCDATA §2.12.6).
type BitmaskRestriction struct {
	Relop   BitmaskRelop
	PropTag PropTag
	Mask    uint32
}

// SizeRestriction compares the byte size of a property (MS-OXCDATA §2.12.8).
type SizeRestriction struct {
	Relop   Relop
	PropTag PropTag
	Size    uint32
}

// ExistRestriction tests for the presence of a property (MS-OXCDATA §2.12.9).
type ExistRestriction struct {
	PropTag PropTag
}

// SubRestriction applies a restriction to a sub-object's message or attachment
// table (MS-OXCDATA §2.12.10).
type SubRestriction struct {
	SubObject uint32
	Res       Restriction
}

// CommentRestriction annotates a child restriction with property values
// (MS-OXCDATA §2.12.4). PropVals must hold at least one value; Res is optional.
type CommentRestriction struct {
	PropVals []TaggedPropVal
	Res      *Restriction
}

// CountRestriction limits how many rows a child restriction may match
// (MS-OXCDATA §2.12.11).
type CountRestriction struct {
	Count  uint32
	SubRes Restriction
}
