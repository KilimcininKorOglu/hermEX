package rop

import (
	"slices"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// Copy-flag bits ([MS-OXCPRPT] 2.2.1.10 / mapidefs MAPI_*). MAPI_MOVE would delete
// the source after the copy (unsupported — it would orphan or duplicate data);
// MAPI_NOREPLACE leaves a destination property that already exists untouched.
const (
	mapiMove      uint8 = 0x01
	mapiNoReplace uint8 = 0x02
)

// object categories for a copy: a copy is only allowed between objects of the same
// category (a message to a message, an attachment to an attachment).
const (
	catNone = iota
	catMessage
	catAttachment
)

// ropCopyProperties handles RopCopyProperties ([MS-OXCPRPT] 2.2.2.10): it copies
// the listed property tags from the source object (the common-header handle) to the
// destination object (the handle at DestHandleIndex).
func (s *Session) ropCopyProperties(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	dhindex, e1 := p.Uint8() // DestHandleIndex
	_, e2 := p.Uint8()       // WantAsynchronous
	copyFlags, e3 := p.Uint8()
	tags, e4 := p.PropTags()
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return false
	}
	return s.copyProperties(ropCopyProperties, out, handles, hindex, dhindex, copyFlags, tags, false)
}

// ropCopyTo handles RopCopyTo ([MS-OXCPRPT] 2.2.2.11): it copies every property of
// the source object to the destination except those in the excluded set. v1 copies
// scalar properties only — a message's attachments and recipients (sub-objects, not
// scalar properties) are not copied.
func (s *Session) ropCopyTo(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	dhindex, e1 := p.Uint8() // DestHandleIndex
	_, e2 := p.Uint8()       // WantAsynchronous
	_, e3 := p.Uint8()       // WantSubObjects (v1 copies scalar properties only)
	copyFlags, e4 := p.Uint8()
	excluded, e5 := p.PropTags()
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return false
	}
	return s.copyProperties(ropCopyTo, out, handles, hindex, dhindex, copyFlags, excluded, true)
}

// copyProperties is the shared body of CopyProperties and CopyTo. With exclude
// false, tags is the inclusive set to copy (CopyProperties); with exclude true,
// tags is the set to skip and everything else is copied (CopyTo). The destination
// must be a writable object of the same category as the source. MAPI_MOVE is
// rejected (deleting the source is unsupported); MAPI_NOREPLACE skips destination
// properties that already exist. The response is an empty problem array (v1 copies
// without per-tag problems), except a null destination reports ecDstNullObject with
// the destination handle index echoed.
func (s *Session) copyProperties(ropID uint8, out *ext.Push, handles []uint32, hindex, dhindex, copyFlags uint8, tags []mapi.PropTag, exclude bool) bool {
	if copyFlags&mapiMove != 0 {
		writeErr(out, ropID, hindex, ecNotSupported)
		return true
	}
	src := s.get(handleAt(handles, hindex))
	if src == nil {
		writeErr(out, ropID, hindex, ecError)
		return true
	}
	dst := s.get(handleAt(handles, dhindex))
	if dst == nil {
		out.Uint8(ropID)
		out.Uint8(hindex)
		out.Uint32(ecDstNullObject)
		out.Uint32(uint32(dhindex)) // NULL_DST1: the destination handle index
		return true
	}
	if objectCategory(src) == catNone || objectCategory(src) != objectCategory(dst) {
		writeErr(out, ropID, hindex, ecDeclineCopy)
		return true
	}

	srcProps, ok := s.objectAllProps(src)
	if !ok {
		writeErr(out, ropID, hindex, ecError)
		return true
	}
	var dstExisting mapi.PropertyValues
	if copyFlags&mapiNoReplace != 0 {
		dstExisting, _ = s.objectAllProps(dst)
	}

	var toCopy mapi.PropertyValues
	for _, pv := range srcProps {
		if tagInSet(tags, pv.Tag) != !exclude {
			// inclusive (CopyProperties): keep only listed; exclusive (CopyTo): drop listed.
			continue
		}
		if copyFlags&mapiNoReplace != 0 {
			if _, present := dstExisting.Get(pv.Tag); present {
				continue
			}
		}
		toCopy = append(toCopy, pv)
	}
	if !s.objectWriteProps(dst, toCopy) {
		writeErr(out, ropID, hindex, ecAccessDenied)
		return true
	}

	out.Uint8(ropID)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(0) // PropertyProblemCount
	return true
}

// objectCategory classifies an object for a copy: message-kinds and
// attachment-kinds form the two copyable categories.
func objectCategory(o *object) int {
	switch o.kind {
	case kindMessage, kindNewMessage, kindEmbedded:
		return catMessage
	case kindAttachment, kindAttachWrite:
		return catAttachment
	}
	return catNone
}

// objectAllProps reads an object's full property bag for a copy source: a stored
// message reads from the store and overlays its buffered edits, so a copy reflects
// the open working copy the same way a read does; the in-memory kinds return their
// bag directly.
func (s *Session) objectAllProps(o *object) (mapi.PropertyValues, bool) {
	switch o.kind {
	case kindMessage:
		props, err := o.store.GetMessageProperties(o.messageID)
		if err != nil {
			return nil, false
		}
		return o.applyPending(props, nil), true
	case kindNewMessage:
		return o.newMsg.props, true
	case kindEmbedded:
		return o.embedded.msg.Props, true
	case kindAttachment:
		return o.attachProps, true
	case kindAttachWrite:
		return o.attachW.pending, true
	}
	return nil, false
}

// objectWriteProps merges properties into a writable object's buffer, the
// destination side of a copy. It reports false for a non-writable object.
func (s *Session) objectWriteProps(o *object, props mapi.PropertyValues) bool {
	var dst *mapi.PropertyValues
	switch o.kind {
	case kindMessage:
		dst = &o.pendingProps
	case kindNewMessage:
		dst = &o.newMsg.props
	case kindEmbedded:
		dst = &o.embedded.msg.Props
	case kindAttachWrite:
		dst = &o.attachW.pending
	default:
		return false
	}
	for _, pv := range props {
		dst.Set(pv.Tag, pv.Value)
	}
	return true
}

// tagInSet reports whether tag appears in tags.
func tagInSet(tags []mapi.PropTag, tag mapi.PropTag) bool {
	return slices.Contains(tags, tag)
}
