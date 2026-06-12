package mapi

import "fmt"

// PropTag is a 32-bit MAPI property tag: the high 16 bits are the property id
// and the low 16 bits are the PropType (MS-OXCDATA §2.9).
type PropTag uint32

// MakeTag composes a property tag from a property id and type, matching the
// MAPI PROP_TAG macro: (id << 16) | type.
func MakeTag(id uint16, t PropType) PropTag {
	return PropTag(uint32(id)<<16 | uint32(t))
}

// ID returns the property id (high 16 bits).
func (tag PropTag) ID() uint16 { return uint16(tag >> 16) }

// Type returns the PropType (low 16 bits).
func (tag PropTag) Type() PropType { return PropType(tag & 0xFFFF) }

// WithType returns tag with its type replaced, matching CHANGE_PROP_TYPE.
func (tag PropTag) WithType(t PropType) PropTag {
	return PropTag(uint32(tag)&^0xFFFF | uint32(t))
}

func (tag PropTag) String() string {
	return fmt.Sprintf("0x%08X(%s)", uint32(tag), tag.Type())
}
