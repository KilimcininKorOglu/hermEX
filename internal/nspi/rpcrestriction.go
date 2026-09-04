package nspi

import (
	"fmt"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// pullRestrictionNDR reads an NSPI Restriction_r ([MS-OXNSPI] 2.2.3) in NDR: the
// GetMatches filter, the only restriction NSPI carries and the only direction
// pulled (the server never sends one). The structure is recursive, every node
// emits its res_type twice (the top frame then the union re-emits the
// discriminant), AND/OR/NOT defer their children through a referent, and a
// property/content node defers a PROPERTY_VALUE. It decodes the kinds the GAL
// matcher evaluates (AND, OR, NOT, PROPERTY, EXIST) plus CONTENT (which the
// matcher treats as no-match, matching the MAPI/HTTP path). A structural kind
// the GAL never receives is a loud error, not a silent wire desync.
func pullRestrictionNDR(p *ndr.Pull) (mapi.Restriction, error) {
	return pullRestrictionDepth(p, 0)
}

// maxRestrictionDepth bounds how deeply a decoded restriction may nest. AND, OR
// and NOT recurse, and a NOT chain costs only twelve wire bytes per level, so an
// unbounded decoder recurses once per few input bytes and a large filter
// overflows the goroutine stack, which is an unrecoverable runtime throw rather
// than an error a caller can catch. Real filters nest a handful deep; this cap
// matches the one the property-model restriction decoder applies.
const maxRestrictionDepth = 100

// pullRestrictionDepth carries the current nesting level so the recursion stays
// bounded.
func pullRestrictionDepth(p *ndr.Pull, depth int) (mapi.Restriction, error) {
	if depth > maxRestrictionDepth {
		return mapi.Restriction{}, fmt.Errorf("%w: restriction nested past the depth limit", ndr.ErrFormat)
	}
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
		// The count is a 32-bit field independent of the transport's body cap, so
		// nothing upstream bounds it: reject one the remaining bytes cannot satisfy
		// before it becomes a make() length, exactly as the sibling array decoders do.
		if err := p.CheckCount(count); err != nil {
			return r, err
		}
		kids := make([]mapi.Restriction, count)
		for i := range kids {
			if kids[i], err = pullRestrictionDepth(p, depth+1); err != nil {
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
			inner, err := pullRestrictionDepth(p, depth+1)
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
