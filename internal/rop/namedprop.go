package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// ropGetPropertyIdsFromNames handles RopGetPropertyIdsFromNames ([MS-OXCPRPT]
// 2.2.2.13): it resolves named properties to their store property ids, allocating
// new ids when the Create flag is set. The flag is 0x02 (MAPI_CREATE) to
// get-or-create or 0x00 to resolve-only (an unknown name maps to id 0); any other
// value is invalid. The named-property map is store-wide, so any object's store
// serves the lookup.
func (s *Session) ropGetPropertyIdsFromNames(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	flags, e1 := p.Uint8()
	names, e2 := p.PropertyNames()
	if e1 != nil || e2 != nil {
		return false
	}
	obj := s.get(handleAt(handles, hindex))
	if obj == nil || obj.store == nil {
		writeErr(out, ropGetPropertyIdsFromNames, hindex, ecError)
		return true
	}
	var create bool
	switch flags {
	case mapiCreate:
		create = true
	case 0x00:
		create = false
	default:
		writeErr(out, ropGetPropertyIdsFromNames, hindex, ecInvalidParam)
		return true
	}
	ids, err := obj.store.GetNamedPropIDs(create, names)
	if err != nil {
		writeErr(out, ropGetPropertyIdsFromNames, hindex, ecError)
		return true
	}

	out.Uint8(ropGetPropertyIdsFromNames)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	_ = out.PropIDs(ids)
	return true
}

// ropGetNamesFromPropertyIds handles RopGetNamesFromPropertyIds ([MS-OXCPRPT]
// 2.2.2.12): it resolves store property ids back to their named properties. An id
// with no mapping (a static id below the named range, or an unknown one) yields a
// PropertyName with the "none" kind — the wire's slot for an unresolved id.
func (s *Session) ropGetNamesFromPropertyIds(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ids, e1 := p.PropIDs()
	if e1 != nil {
		return false
	}
	obj := s.get(handleAt(handles, hindex))
	if obj == nil || obj.store == nil {
		writeErr(out, ropGetNamesFromPropertyIds, hindex, ecError)
		return true
	}
	names := make([]mapi.PropertyName, len(ids))
	for i, id := range ids {
		name, ok, err := obj.store.NamedPropName(id)
		if err != nil {
			writeErr(out, ropGetNamesFromPropertyIds, hindex, ecError)
			return true
		}
		if ok {
			names[i] = name
		} else {
			names[i] = mapi.PropertyName{Kind: mapi.KindNone}
		}
	}

	out.Uint8(ropGetNamesFromPropertyIds)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	_ = out.PropertyNames(names)
	return true
}
