package rop

import (
	"slices"

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
	// A delegate may read a message only with ReadAny on its REAL parent folder,
	// resolved from the store — never the wire FolderId, which is informational and
	// could name a folder the caller can read to reach one they cannot. The lookup
	// also serves as the existence check (ErrNotFound → ecNotFound), so an owner,
	// who is unrestricted, sees identical behavior.
	parentFID, err := parent.store.MessageFolder(msgID)
	if err != nil {
		writeErr(out, ropOpenMessage, ohindex, ecNotFound)
		return true
	}
	if ok, err := s.authorize(parent.store, parentFID, mapi.FrightsReadAny); err != nil {
		writeErr(out, ropOpenMessage, ohindex, ecError)
		return true
	} else if !ok {
		writeErr(out, ropOpenMessage, ohindex, ecAccessDenied)
		return true
	}
	msg, err := parent.store.OpenMessage(msgID)
	if err != nil {
		writeErr(out, ropOpenMessage, ohindex, ecNotFound)
		return true
	}
	// folderID caches the message's real parent folder so a later write on this
	// handle (SetProperties, SaveChanges, …) authorizes against the right folder's
	// permissions without re-resolving it.
	h := s.alloc(&object{kind: kindMessage, store: parent.store, messageID: msgID, folderID: parentFID})
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

// readMessageProps returns the requested properties of a message-kind object. An
// opened store message reads from the store and then overlays its buffered edits
// so a read reflects the open working copy (MAPI's read-your-writes contract: a
// SetProperties/DeleteProperties before SaveChangesMessage is visible to a
// GetProperties on the same handle). An embedded message reads from the in-memory
// message imported from its parent attachment's encapsulated bytes. An empty tag
// list returns the full bag (matching GetMessageProperties). The bool reports
// whether the object is a readable message kind.
func (o *object) readMessageProps(tags ...mapi.PropTag) (mapi.PropertyValues, bool, error) {
	switch o.kind {
	case kindMessage:
		if o.store == nil {
			return nil, false, nil
		}
		props, err := o.store.GetMessageProperties(o.messageID, tags...)
		if err != nil {
			return nil, true, err
		}
		return o.applyPending(props, tags), true, nil
	case kindEmbedded:
		if o.embedded == nil || o.embedded.msg == nil {
			return nil, false, nil
		}
		return selectProps(o.embedded.msg.Props, tags), true, nil
	}
	return nil, false, nil
}

// applyPending overlays an opened message's buffered edits onto a freshly read
// store property bag so a read reflects the working copy: a buffered delete drops
// its tag, a buffered set overrides (or adds) its value. tags is the read's
// requested tag filter (empty = all); a buffered set is surfaced only when it
// falls within that filter, matching how the store read itself narrows. After fix
// of the set/delete buffers, the two are mutually exclusive per tag, so the order
// (deletes then sets) is incidental.
func (o *object) applyPending(props mapi.PropertyValues, tags []mapi.PropTag) mapi.PropertyValues {
	if len(o.pendingDeletes) == 0 && len(o.pendingProps) == 0 {
		return props
	}
	for _, t := range o.pendingDeletes {
		props = removeTag(props, t)
	}
	for _, pv := range o.pendingProps {
		if len(tags) == 0 || slices.Contains(tags, pv.Tag) {
			props.Set(pv.Tag, pv.Value)
		}
	}
	return props
}

// selectProps narrows a property bag to the requested tags, keeping only the ones
// present; an empty tag list returns the whole bag. It mirrors the store's
// GetMessageProperties shape so the read ROPs treat an in-memory embedded message
// the same as a stored one.
func selectProps(all mapi.PropertyValues, tags []mapi.PropTag) mapi.PropertyValues {
	if len(tags) == 0 {
		return all
	}
	var out mapi.PropertyValues
	for _, t := range tags {
		if v, ok := all.Get(t); ok {
			out.Set(t, v)
		}
	}
	return out
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
	if msg == nil {
		writeErr(out, ropGetPropertiesSpecific, hindex, ecError)
		return true
	}
	props, ok, err := msg.readMessageProps(cols...)
	if !ok || err != nil {
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
	if msg == nil {
		writeErr(out, ropGetPropertiesAll, hindex, ecError)
		return true
	}
	props, ok, err := msg.readMessageProps()
	if !ok || err != nil {
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
