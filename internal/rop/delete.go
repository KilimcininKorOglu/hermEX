package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// ropDeleteProperties handles RopDeleteProperties ([MS-OXCPRPT] 2.2.2.7) and
// ropDeletePropertiesNoReplicate handles RopDeletePropertiesNoReplicate
// (2.2.2.8) — identical save-replication aside, which is a no-op on a single
// replica. Both remove the request's property tags from the open object.
func (s *Session) ropDeleteProperties(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	return s.deleteProperties(ropDeleteProperties, p, out, handles, hindex)
}

func (s *Session) ropDeletePropertiesNoReplicate(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	return s.deleteProperties(ropDeletePropertiesNoReplicate, p, out, handles, hindex)
}

// deleteProperties removes the request's property tags from the open object. On an
// opened message the removals buffer until SaveChangesMessage (the in-place edit
// counterpart of buffered SetProperties, reallocating the change number on save);
// on an in-memory message, embedded message, or created attachment the tags are
// dropped from the in-memory bag. A delete also drops any buffered set for the same
// tag so a delete-after-set wins. The response carries an empty problem array — v1
// removes every requested tag without enforcing read-only/sticky-tag protection.
func (s *Session) deleteProperties(ropID uint8, p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	tags, e1 := p.PropTags()
	if e1 != nil {
		return false
	}
	obj := s.get(handleAt(handles, hindex))
	if obj == nil {
		writeErr(out, ropID, hindex, ecError)
		return true
	}
	switch obj.kind {
	case kindMessage:
		obj.pendingDeletes = append(obj.pendingDeletes, tags...)
		for _, t := range tags {
			obj.pendingProps = removeTag(obj.pendingProps, t)
		}
	case kindNewMessage:
		for _, t := range tags {
			obj.newMsg.props = removeTag(obj.newMsg.props, t)
		}
	case kindEmbedded:
		for _, t := range tags {
			obj.embedded.msg.Props = removeTag(obj.embedded.msg.Props, t)
		}
	case kindAttachWrite:
		for _, t := range tags {
			obj.attachW.pending = removeTag(obj.attachW.pending, t)
		}
	default:
		writeErr(out, ropID, hindex, ecNotSupported)
		return true
	}

	out.Uint8(ropID)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(0) // PropertyProblemCount
	return true
}

// removeTag returns a copy of pv with every entry for tag removed.
func removeTag(pv mapi.PropertyValues, tag mapi.PropTag) mapi.PropertyValues {
	var out mapi.PropertyValues
	for _, p := range pv {
		if p.Tag != tag {
			out = append(out, p)
		}
	}
	return out
}

// dropDeleteTag returns a copy of tags with every occurrence of tag removed — the
// PropTag-slice counterpart of removeTag, used when a buffered set supersedes a
// pending delete for the same tag.
func dropDeleteTag(tags []mapi.PropTag, tag mapi.PropTag) []mapi.PropTag {
	var out []mapi.PropTag
	for _, t := range tags {
		if t != tag {
			out = append(out, t)
		}
	}
	return out
}
