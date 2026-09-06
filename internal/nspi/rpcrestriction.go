package nspi

import (
	"fmt"
	"math"

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

// pullPtrRestrictionNDR reads a unique-pointer restriction, answering nil when
// the referent is null.
func pullPtrRestrictionNDR(p *ndr.Pull) (*mapi.Restriction, error) {
	ref, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if ref == 0 {
		return nil, nil
	}
	r, err := pullRestrictionNDR(p)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// pullRestrictionDepth carries the current nesting level so the recursion stays
// bounded.
func pullRestrictionDepth(p *ndr.Pull, depth int) (mapi.Restriction, error) {
	if depth > maxRestrictionDepth {
		return mapi.Restriction{}, fmt.Errorf("%w: restriction nested past the depth limit", ndr.ErrFormat)
	}
	resType, err := pullRestrictionTypeNDR(p)
	if err != nil {
		return mapi.Restriction{}, err
	}
	// #nosec G115 -- pullRestrictionTypeNDR refuses a discriminant above MaxUint8, so the value here always fits
	r := mapi.Restriction{Type: mapi.RestrictionType(resType)}
	switch r.Type {
	case mapi.ResAnd, mapi.ResOr:
		r.Value, err = pullSubRestrictions(p, depth)
	case mapi.ResNot:
		r.Value, err = pullNotRestriction(p, depth)
	case mapi.ResContent:
		r.Value, err = pullContentRestriction(p)
	case mapi.ResProperty:
		r.Value, err = pullPropertyRestriction(p)
	case mapi.ResExist:
		r.Value, err = pullExistRestriction(p)
	default:
		return r, fmt.Errorf("%w: NSPI restriction type %#x unsupported", ndr.ErrFormat, resType)
	}
	if err != nil {
		return r, err
	}
	return r, nil
}

// pullRestrictionTypeNDR reads a restriction node's discriminant, which the union
// re-emits, and refuses a value the type cannot hold. The wire carries it in 32
// bits and RestrictionType is 8, so a value above the byte range would truncate
// into a structural kind the decoder accepts, and the node would then be parsed
// with the wrong layout.
func pullRestrictionTypeNDR(p *ndr.Pull) (uint32, error) {
	resType, err := p.Uint32()
	if err != nil {
		return 0, err
	}
	resType2, err := p.Uint32()
	if err != nil {
		return 0, err
	}
	if resType2 != resType {
		return 0, fmt.Errorf("%w: restriction type %d != union type %d", ndr.ErrFormat, resType, resType2)
	}
	if resType > math.MaxUint8 {
		return 0, fmt.Errorf("%w: restriction type %#x out of range", ndr.ErrFormat, resType)
	}
	return resType, nil
}

// pullSubRestrictions decodes the child list of an AND or OR node. A nil value is
// a null children referent, which is an empty node.
func pullSubRestrictions(p *ndr.Pull, depth int) (any, error) {
	cres, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	ref, err := p.Uint32() // children referent
	if err != nil {
		return nil, err
	}
	if ref == 0 {
		return nil, nil
	}
	count, err := p.Uint32() // conformant max_count, equals cres
	if err != nil {
		return nil, err
	}
	if count != cres {
		return nil, fmt.Errorf("%w: restriction child count %d != %d", ndr.ErrFormat, count, cres)
	}
	// The count is a 32-bit field independent of the transport's body cap, so
	// nothing upstream bounds it: reject one the remaining bytes cannot satisfy
	// before it becomes a make() length, exactly as the sibling array decoders do.
	if err := p.CheckCount(count); err != nil {
		return nil, err
	}
	kids := make([]mapi.Restriction, count)
	for i := range kids {
		if kids[i], err = pullRestrictionDepth(p, depth+1); err != nil {
			return nil, err
		}
	}
	return kids, nil
}

// pullNotRestriction decodes the single child of a NOT node. A nil value is a
// null referent, which negates nothing.
func pullNotRestriction(p *ndr.Pull, depth int) (any, error) {
	ref, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if ref == 0 {
		return nil, nil
	}
	return pullRestrictionDepth(p, depth+1)
}

// pullContentRestriction decodes a content (substring match) node.
func pullContentRestriction(p *ndr.Pull) (any, error) {
	var c mapi.ContentRestriction
	var err error
	if c.FuzzyLevel, err = p.Uint32(); err != nil {
		return nil, err
	}
	tag, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	c.PropTag = mapi.PropTag(tag)
	ref, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if ref != 0 {
		if c.PropVal, err = pullPropValNDR(p); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// pullPropertyRestriction decodes a property comparison node.
func pullPropertyRestriction(p *ndr.Pull) (any, error) {
	var pr mapi.PropertyRestriction
	relop, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	// Relop is 8 bits, so an out-of-range wire value would truncate into a
	// different, valid comparison rather than being refused.
	if relop > math.MaxUint8 {
		return nil, fmt.Errorf("%w: relational operator %#x out of range", ndr.ErrFormat, relop)
	}
	pr.Relop = mapi.Relop(relop)
	tag, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	pr.PropTag = mapi.PropTag(tag)
	ref, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if ref != 0 {
		if pr.PropVal, err = pullPropValNDR(p); err != nil {
			return nil, err
		}
	}
	return pr, nil
}

// pullExistRestriction decodes a property-exists node, whose two reserved fields
// are read and discarded.
func pullExistRestriction(p *ndr.Pull) (any, error) {
	if _, err := p.Uint32(); err != nil { // reserved1
		return nil, err
	}
	tag, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if _, err := p.Uint32(); err != nil { // reserved2
		return nil, err
	}
	return mapi.ExistRestriction{PropTag: mapi.PropTag(tag)}, nil
}
