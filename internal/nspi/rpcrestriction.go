package nspi

import (
	"fmt"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// pullRestrictionNDR reads an NSPI Restriction_r ([MS-OXNSPI] 2.2.3) in NDR: the
// GetMatches filter, the only restriction NSPI carries and the only direction
// pulled (the server never sends one). The structure is recursive — every node
// emits its res_type twice (the top frame then the union re-emits the
// discriminant), AND/OR/NOT defer their children through a referent, and a
// property/content node defers a PROPERTY_VALUE. It decodes the kinds the GAL
// matcher evaluates (AND, OR, NOT, PROPERTY, EXIST) plus CONTENT (which the
// matcher treats as no-match, matching the MAPI/HTTP path). A structural kind
// the GAL never receives is a loud error, not a silent wire desync.
func pullRestrictionNDR(p *ndr.Pull) (mapi.Restriction, error) {
	resType, err := p.Uint32()
	if err != nil {
		return mapi.Restriction{}, err
	}
	resType2, err := p.Uint32() // the union re-emits the discriminant
	if err != nil {
		return mapi.Restriction{}, err
	}
	if resType2 != resType {
		return mapi.Restriction{}, fmt.Errorf("%w: restriction type %d != union type %d", ndr.ErrFormat, resType, resType2)
	}
	r := mapi.Restriction{Type: mapi.RestrictionType(resType)}
	switch r.Type {
	case mapi.ResAnd, mapi.ResOr:
		cres, err := p.Uint32()
		if err != nil {
			return r, err
		}
		ref, err := p.Uint32() // children referent
		if err != nil {
			return r, err
		}
		if ref == 0 {
			return r, nil
		}
		count, err := p.Uint32() // conformant max_count, equals cres
		if err != nil {
			return r, err
		}
		if count != cres {
			return r, fmt.Errorf("%w: restriction child count %d != %d", ndr.ErrFormat, count, cres)
		}
		kids := make([]mapi.Restriction, count)
		for i := range kids {
			if kids[i], err = pullRestrictionNDR(p); err != nil {
				return r, err
			}
		}
		r.Value = kids
	case mapi.ResNot:
		ref, err := p.Uint32()
		if err != nil {
			return r, err
		}
		if ref != 0 {
			inner, err := pullRestrictionNDR(p)
			if err != nil {
				return r, err
			}
			r.Value = inner
		}
	case mapi.ResContent:
		var c mapi.ContentRestriction
		if c.FuzzyLevel, err = p.Uint32(); err != nil {
			return r, err
		}
		tag, err := p.Uint32()
		if err != nil {
			return r, err
		}
		c.PropTag = mapi.PropTag(tag)
		ref, err := p.Uint32()
		if err != nil {
			return r, err
		}
		if ref != 0 {
			if c.PropVal, err = pullPropValNDR(p); err != nil {
				return r, err
			}
		}
		r.Value = c
	case mapi.ResProperty:
		var pr mapi.PropertyRestriction
		relop, err := p.Uint32()
		if err != nil {
			return r, err
		}
		pr.Relop = mapi.Relop(relop)
		tag, err := p.Uint32()
		if err != nil {
			return r, err
		}
		pr.PropTag = mapi.PropTag(tag)
		ref, err := p.Uint32()
		if err != nil {
			return r, err
		}
		if ref != 0 {
			if pr.PropVal, err = pullPropValNDR(p); err != nil {
				return r, err
			}
		}
		r.Value = pr
	case mapi.ResExist:
		if _, err = p.Uint32(); err != nil { // reserved1
			return r, err
		}
		tag, err := p.Uint32()
		if err != nil {
			return r, err
		}
		if _, err = p.Uint32(); err != nil { // reserved2
			return r, err
		}
		r.Value = mapi.ExistRestriction{PropTag: mapi.PropTag(tag)}
	default:
		return r, fmt.Errorf("%w: NSPI restriction type %#x unsupported", ndr.ErrFormat, resType)
	}
	return r, nil
}
