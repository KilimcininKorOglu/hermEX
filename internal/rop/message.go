package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// TYPED_STRING string types ([MS-OXCDATA] 2.11.4 / p_typed_str).
const (
	stringTypeEmpty   uint8 = 0x1
	stringTypeUnicode uint8 = 0x4
)

// pushTypedString writes a TYPED_STRING: a 1-byte type then, for UNICODE, the
// UTF-16LE string. An empty value is encoded as EMPTY (present but empty) with
// no body — matching the reference rop_openmessage, which uses EMPTY rather than
// NONE for an absent subject prefix / normalized subject.
func pushTypedString(out *ext.Push, s string) {
	if s == "" {
		out.Uint8(stringTypeEmpty)
		return
	}
	out.Uint8(stringTypeUnicode)
	out.Unicode(s)
}

// stringProp returns a string-typed property value, or "" when absent.
func stringProp(pv mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := pv.Get(tag); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ropOpenMessage handles RopOpenMessage ([MS-OXCMSG] 2.2.3.1): it resolves the
// message entry id, registers a message object, and writes the open response —
// the subject TYPED_STRINGs and (v1) an empty recipient table. The 64-bit
// MessageId is an EID whose global-counter value is the objectstore id; the
// FolderId is informational since message ids are store-global.
func (s *Session) ropOpenMessage(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()     // OutputHandleIndex
	_, e2 := p.Uint16()          // Cpid
	_, e3 := p.Uint64()          // FolderId
	_, e4 := p.Uint8()           // OpenModeFlags
	messageEID, e5 := p.Uint64() // MessageId
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return false
	}
	parent := s.get(handleAt(handles, hindex))
	if parent == nil || parent.store == nil {
		writeErr(out, ropOpenMessage, ohindex, ecError)
		return true
	}
	msgID := int64(mapi.EID(messageEID).GCValue())
	msg, err := parent.store.OpenMessage(msgID)
	if err != nil {
		writeErr(out, ropOpenMessage, ohindex, ecNotFound)
		return true
	}
	h := s.alloc(&object{kind: kindMessage, store: parent.store, messageID: msgID})
	setHandle(handles, ohindex, h)

	out.Uint8(ropOpenMessage)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // HasNamedProperties (v1 advertises none)
	pushTypedString(out, stringProp(msg.Props, mapi.PrSubjectPrefix))
	pushTypedString(out, stringProp(msg.Props, mapi.PrNormalizedSubject))
	out.Uint16(0)         // RecipientCount (v1: inline recipient table deferred)
	_ = out.PropTags(nil) // RecipientColumns (empty proptag array)
	out.Uint8(0)          // RowCount
	return true
}

// ropGetPropertiesSpecific handles RopGetPropertiesSpecific ([MS-OXCPRPT]
// 2.2.2.10): it returns the requested columns of the open message as a single
// PROPERTY_ROW.
func (s *Session) ropGetPropertiesSpecific(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.Uint16() // PropertySizeLimit
	_, e2 := p.Uint16() // WantUnicode
	cols, e3 := p.PropTags()
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	msg := s.get(handleAt(handles, hindex))
	if msg == nil || msg.kind != kindMessage || msg.store == nil {
		writeErr(out, ropGetPropertiesSpecific, hindex, ecError)
		return true
	}
	props, err := msg.store.GetMessageProperties(msg.messageID, cols...)
	if err != nil {
		writeErr(out, ropGetPropertiesSpecific, hindex, ecError)
		return true
	}
	// Build the row first so a serialization failure does not leave a partial
	// response after the header.
	row := ext.NewPush(ext.FlagUTF16)
	if err := buildPropertyRow(row, cols, props); err != nil {
		writeErr(out, ropGetPropertiesSpecific, hindex, ecError)
		return true
	}
	out.Uint8(ropGetPropertiesSpecific)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Raw(row.Bytes())
	return true
}

// ropGetPropertiesAll handles RopGetPropertiesAll ([MS-OXCPRPT] 2.2.2.9): it
// returns the open message's full property bag as a TPROPVAL_ARRAY. v1 does not
// honor the size limit (large body properties are returned inline).
func (s *Session) ropGetPropertiesAll(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.Uint16() // PropertySizeLimit
	_, e2 := p.Uint16() // WantUnicode
	if e1 != nil || e2 != nil {
		return false
	}
	msg := s.get(handleAt(handles, hindex))
	if msg == nil || msg.kind != kindMessage || msg.store == nil {
		writeErr(out, ropGetPropertiesAll, hindex, ecError)
		return true
	}
	props, err := msg.store.GetMessageProperties(msg.messageID)
	if err != nil {
		writeErr(out, ropGetPropertiesAll, hindex, ecError)
		return true
	}
	body := ext.NewPush(ext.FlagUTF16)
	if err := body.PropertyValues(props); err != nil {
		writeErr(out, ropGetPropertiesAll, hindex, ecError)
		return true
	}
	out.Uint8(ropGetPropertiesAll)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Raw(body.Bytes())
	return true
}
