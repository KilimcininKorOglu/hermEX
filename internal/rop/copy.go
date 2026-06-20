package rop

import (
	"slices"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
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
	// CopyProperties has no WantSubObjects flag: a sub-object collection is copied
	// only when its tag (PR_MESSAGE_RECIPIENTS/PR_MESSAGE_ATTACHMENTS) is named in
	// the inclusive list, so wantSub is irrelevant here (passed false).
	return s.copyProperties(ropCopyProperties, out, handles, hindex, dhindex, copyFlags, tags, false, false)
}

// ropCopyTo handles RopCopyTo ([MS-OXCPRPT] 2.2.2.11): it copies every property of
// the source object to the destination except those in the excluded set. For a
// message source with WantSubObjects set, it also copies the recipients and
// attachments (sub-objects), each suppressible by excluding its collection tag
// PR_MESSAGE_RECIPIENTS / PR_MESSAGE_ATTACHMENTS.
func (s *Session) ropCopyTo(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	dhindex, e1 := p.Uint8() // DestHandleIndex
	_, e2 := p.Uint8()       // WantAsynchronous
	wantSub, e3 := p.Uint8() // WantSubObjects
	copyFlags, e4 := p.Uint8()
	excluded, e5 := p.PropTags()
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return false
	}
	return s.copyProperties(ropCopyTo, out, handles, hindex, dhindex, copyFlags, excluded, true, wantSub != 0)
}

// copyProperties is the shared body of CopyProperties and CopyTo. With exclude
// false, tags is the inclusive set to copy (CopyProperties); with exclude true,
// tags is the set to skip and everything else is copied (CopyTo). The destination
// must be a writable object of the same category as the source. MAPI_MOVE is
// rejected (deleting the source is unsupported); MAPI_NOREPLACE skips destination
// properties that already exist. The response is an empty problem array (v1 copies
// without per-tag problems), except a null destination reports ecDstNullObject with
// the destination handle index echoed.
func (s *Session) copyProperties(ropID uint8, out *ext.Push, handles []uint32, hindex, dhindex, copyFlags uint8, tags []mapi.PropTag, exclude, wantSub bool) bool {
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
	// A property copy writes the destination object — SetProperties reached through a
	// different entry — so it gates the same way: editing an existing stored message
	// requires EditAny on its folder. A compose message, a created attachment, and a
	// composed embedded message are gated at their own create chokepoints and persist
	// only through the parent message's gated save, so they take no copy-time gate.
	if dst.kind == kindMessage && s.denyWrite(out, ropID, hindex, dst.store, dst.folderID, mapi.FrightsEditAny) {
		return true
	}

	// Decide the sub-object copy up front (only meaningful for a message source):
	// a CopyTo with WantSubObjects copies each collection unless its tag is excluded;
	// a CopyProperties copies a collection only when its tag is explicitly listed.
	// If sub-objects are requested but the destination cannot stage them (only a
	// compose message can), refuse before applying anything.
	copyRecips, copyAttachs := false, false
	if objectCategory(src) == catMessage {
		copyRecips, copyAttachs = subObjectsToCopy(exclude, wantSub, tags)
	}
	if (copyRecips || copyAttachs) && !canWriteSubObjects(dst) {
		writeErr(out, ropID, hindex, ecNotSupported)
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
		if tagInSet(copyMetaExcluded, pv.Tag) {
			// Server-owned identity/versioning and computed props are never copied;
			// the destination gets its own at save (see copyMetaExcluded).
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

	if copyRecips || copyAttachs {
		recips, attachs, ok := s.objectSubObjects(src)
		if !ok {
			writeErr(out, ropID, hindex, ecError)
			return true
		}
		if !s.objectWriteSubObjects(dst, copyRecips, recips, copyAttachs, attachs) {
			writeErr(out, ropID, hindex, ecNotSupported)
			return true
		}
	}

	out.Uint8(ropID)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(0) // PropertyProblemCount
	return true
}

// copyMetaExcluded are the server-owned identity/versioning and computed
// properties a copy must never carry: the destination is assigned its own
// identity and change tracking when it is saved. Mirrors the reference copy_to
// strip list ([message_object] copy_to) for the tags hermEX models. (For a stored
// message source these are columnar/synthesized and never appear in the property
// bag anyway; the strip also covers an in-memory compose/embedded source.)
var copyMetaExcluded = []mapi.PropTag{
	mapi.PrMid, mapi.PrChangeKey, mapi.PrChangeNumber,
	mapi.PrPredecessorChangeList, mapi.PrSearchKey, mapi.PrMessageSize,
}

// subObjectsToCopy decides whether a message copy includes its recipient and
// attachment collections. For CopyTo (exclude) the collections ride along when
// WantSubObjects is set, each suppressed by excluding its tag; for CopyProperties
// a collection is copied only when its tag is named in the inclusive list.
func subObjectsToCopy(exclude, wantSub bool, tags []mapi.PropTag) (recips, attachs bool) {
	if exclude {
		return wantSub && !tagInSet(tags, mapi.PrMessageRecipients),
			wantSub && !tagInSet(tags, mapi.PrMessageAttachments)
	}
	return tagInSet(tags, mapi.PrMessageRecipients), tagInSet(tags, mapi.PrMessageAttachments)
}

// canWriteSubObjects reports whether a copy destination can stage copied
// recipients/attachments. Only a compose message (in memory until save) can: a
// new message buffers them for its CreateMessage, and a composed embedded message
// carries them in its in-memory message. A persisted opened message has no
// recipient-staging path, so sub-objects cannot be copied onto it.
func canWriteSubObjects(o *object) bool {
	return o.kind == kindNewMessage || o.kind == kindEmbedded
}

// objectSubObjects reads a message source's recipient and attachment property
// bags for a copy: a stored message reloads them from the store (with attachment
// content), the in-memory kinds return their staged bags.
func (s *Session) objectSubObjects(o *object) (recips, attachs []mapi.PropertyValues, ok bool) {
	switch o.kind {
	case kindMessage:
		msg, err := o.store.OpenMessage(o.messageID)
		if err != nil {
			return nil, nil, false
		}
		for _, a := range msg.Attachments {
			attachs = append(attachs, a.Props)
		}
		return msg.Recipients, attachs, true
	case kindNewMessage:
		for _, a := range o.newMsg.attachments {
			attachs = append(attachs, a.props)
		}
		return o.newMsg.recipients, attachs, true
	case kindEmbedded:
		for _, a := range o.embedded.msg.Attachments {
			attachs = append(attachs, a.Props)
		}
		return o.embedded.msg.Recipients, attachs, true
	}
	return nil, nil, false
}

// objectWriteSubObjects replaces a compose destination's recipient/attachment
// collections with the copied bags (a copy reproduces the source's collections).
// Each is gated by its own flag so an excluded collection is left untouched.
// Returns false for a destination that cannot stage sub-objects.
func (s *Session) objectWriteSubObjects(o *object, copyRecips bool, recips []mapi.PropertyValues, copyAttachs bool, attachs []mapi.PropertyValues) bool {
	switch o.kind {
	case kindNewMessage:
		nm := o.newMsg
		if copyRecips {
			nm.recipients = cloneRecipients(recips)
		}
		if copyAttachs {
			nm.attachments = nil
			for _, ap := range attachs {
				num := nextNewAttachNum(nm.attachments)
				nm.attachments = append(nm.attachments, &newAttachment{attachNum: num, props: cloneAttachProps(ap, num)})
			}
		}
		return true
	case kindEmbedded:
		em := o.embedded.msg
		if copyRecips {
			em.Recipients = cloneRecipients(recips)
		}
		if copyAttachs {
			em.Attachments = nil
			for _, ap := range attachs {
				em.Attachments = append(em.Attachments, oxcmail.Attachment{Props: append(mapi.PropertyValues(nil), ap...)})
			}
		}
		return true
	}
	return false
}

// cloneRecipients deep-copies recipient bags so the destination owns its copy.
func cloneRecipients(recips []mapi.PropertyValues) []mapi.PropertyValues {
	out := make([]mapi.PropertyValues, len(recips))
	for i, r := range recips {
		out[i] = append(mapi.PropertyValues(nil), r...)
	}
	return out
}

// cloneAttachProps deep-copies an attachment bag for the destination, re-stamping
// PR_ATTACH_NUM with the destination's own number (the source's row index does not
// carry over).
func cloneAttachProps(src mapi.PropertyValues, num uint32) mapi.PropertyValues {
	out := make(mapi.PropertyValues, 0, len(src))
	for _, pv := range src {
		if pv.Tag == mapi.PrAttachNum {
			continue
		}
		out = append(out, pv)
	}
	out.Set(mapi.PrAttachNum, int32(num))
	return out
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
