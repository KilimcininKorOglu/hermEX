package ext

import (
	"errors"

	"hermex/internal/mapi"
)

// maxRestrictionDepth bounds how deeply a decoded restriction may nest. A search
// restriction over attacker bytes can chain single-child nodes (NOT/SUB/COMMENT/
// COUNT) one per byte, so an unbounded decoder recurses once per input byte and a
// large filter overflows the goroutine stack, an unrecoverable runtime throw. Real
// filters nest only a handful deep; this cap is far above any legitimate one.
const maxRestrictionDepth = 100

// errRestrictionTooDeep reports a restriction nested past maxRestrictionDepth.
var errRestrictionTooDeep = errors.New("restriction nested past the depth limit")

// Restriction writes a search restriction: a one-byte type tag
// followed by the payload for that type. AND/OR child counts follow FlagWCount
// (u32 set, u16 clear); COMMENT carries a u8 count (at least one); COUNT carries
// a u32. The recursion bottoms out at ResNull, which has no payload.
func (p *Push) Restriction(r mapi.Restriction) error {
	p.Uint8(uint8(r.Type))
	write, ok := restrictionWriters[r.Type]
	if !ok {
		return ErrFormat
	}
	return write(p, r.Value)
}

// restrictionWriter writes one restriction node's payload; the type tag is
// already on the wire.
type restrictionWriter func(p *Push, v any) error

// restrictionWriters is the node vocabulary the encoder writes. It is populated
// in init because every branching node writes its children back through
// Push.Restriction, which reads this table.
var restrictionWriters map[mapi.RestrictionType]restrictionWriter

func init() {
	restrictionWriters = map[mapi.RestrictionType]restrictionWriter{
		mapi.ResAnd:        pushTyped(pushResChildren),
		mapi.ResOr:         pushTyped(pushResChildren),
		mapi.ResNot:        pushTyped((*Push).Restriction),
		mapi.ResComment:    pushTyped(pushResComment),
		mapi.ResAnnotation: pushTyped(pushResComment),
		mapi.ResContent: pushTyped(func(p *Push, c mapi.ContentRestriction) error {
			p.Uint32(c.FuzzyLevel)
			p.Uint32(uint32(c.PropTag))
			return p.TaggedPropVal(c.PropVal)
		}),
		mapi.ResProperty: pushTyped(func(p *Push, pr mapi.PropertyRestriction) error {
			p.Uint8(uint8(pr.Relop))
			p.Uint32(uint32(pr.PropTag))
			return p.TaggedPropVal(pr.PropVal)
		}),
		mapi.ResPropCompare: pushTyped(func(p *Push, pc mapi.ComparePropsRestriction) error {
			p.Uint8(uint8(pc.Relop))
			p.Uint32(uint32(pc.PropTag1))
			p.Uint32(uint32(pc.PropTag2))
			return nil
		}),
		mapi.ResBitmask: pushTyped(func(p *Push, b mapi.BitmaskRestriction) error {
			p.Uint8(uint8(b.Relop))
			p.Uint32(uint32(b.PropTag))
			p.Uint32(b.Mask)
			return nil
		}),
		mapi.ResSize: pushTyped(func(p *Push, s mapi.SizeRestriction) error {
			p.Uint8(uint8(s.Relop))
			p.Uint32(uint32(s.PropTag))
			p.Uint32(s.Size)
			return nil
		}),
		mapi.ResExist: pushTyped(func(p *Push, e mapi.ExistRestriction) error {
			p.Uint32(uint32(e.PropTag))
			return nil
		}),
		mapi.ResSub: pushTyped(func(p *Push, s mapi.SubRestriction) error {
			p.Uint32(s.SubObject)
			return p.Restriction(s.Res)
		}),
		mapi.ResCount: pushTyped(func(p *Push, c mapi.CountRestriction) error {
			p.Uint32(c.Count)
			return p.Restriction(c.SubRes)
		}),
		// The recursion bottoms out here: ResNull has no payload.
		mapi.ResNull: func(*Push, any) error { return nil },
	}
}

// pushResChildren writes an AND/OR child list: the count, then each child.
func pushResChildren(p *Push, kids []mapi.Restriction) error {
	if err := pushResCount(p, len(kids)); err != nil {
		return err
	}
	for _, k := range kids {
		if err := p.Restriction(k); err != nil {
			return err
		}
	}
	return nil
}

// pushResCount writes an AND/OR child count, whose width follows FlagWCount
// (u32 set, u16 clear).
func pushResCount(p *Push, n int) error {
	if p.flags&FlagWCount != 0 {
		// #nosec G115 -- a Go slice length; the buffer it measures is orders of magnitude below the field
		p.Uint32(uint32(n))
		return nil
	}
	if n > 0xFFFF {
		return ErrFormat
	}
	// #nosec G115 -- the length is bounded before it reaches the field, by the range check above it or by the 16-bit prefix the bytes were read with
	p.Uint16(uint16(n))
	return nil
}

// pushResComment writes a COMMENT/ANNOTATION node: a u8 count of property values
// (at least one), those values, and an optional nested restriction.
func pushResComment(p *Push, c mapi.CommentRestriction) error {
	if len(c.PropVals) == 0 || len(c.PropVals) > 0xFF {
		return ErrFormat
	}
	// #nosec G115 -- the length is bounded before it reaches the field, by the range check above it or by the 16-bit prefix the bytes were read with
	p.Uint8(uint8(len(c.PropVals)))
	for _, pv := range c.PropVals {
		if err := p.TaggedPropVal(pv); err != nil {
			return err
		}
	}
	if c.Res == nil {
		p.Uint8(0)
		return nil
	}
	p.Uint8(1)
	return p.Restriction(*c.Res)
}

// Restriction reads a search restriction, mirroring the type
// dispatch and count-width rules of Push.Restriction.
func (p *Pull) Restriction() (mapi.Restriction, error) {
	p.resDepth++
	defer func() { p.resDepth-- }()
	if p.resDepth > maxRestrictionDepth {
		return mapi.Restriction{}, errRestrictionTooDeep
	}
	rt, err := p.Uint8()
	if err != nil {
		return mapi.Restriction{}, err
	}
	r := mapi.Restriction{Type: mapi.RestrictionType(rt)}
	read, ok := restrictionReaders[r.Type]
	if !ok {
		return r, ErrFormat
	}
	v, err := read(p)
	if err != nil {
		return r, err
	}
	r.Value = v
	return r, nil
}

// restrictionReader reads one restriction node's payload, returning the Go type
// that node carries in mapi.Restriction.Value.
type restrictionReader func(p *Pull) (any, error)

// restrictionReaders is the node vocabulary the decoder reads. Like the writer
// table it is populated in init, because every branching node reads its children
// back through Pull.Restriction.
var restrictionReaders map[mapi.RestrictionType]restrictionReader

func init() {
	restrictionReaders = map[mapi.RestrictionType]restrictionReader{
		mapi.ResAnd:         pullResChildren,
		mapi.ResOr:          pullResChildren,
		mapi.ResNot:         pullTyped((*Pull).Restriction),
		mapi.ResComment:     pullResComment,
		mapi.ResAnnotation:  pullResComment,
		mapi.ResContent:     pullResContent,
		mapi.ResProperty:    pullResProperty,
		mapi.ResPropCompare: pullResPropCompare,
		mapi.ResBitmask: func(p *Pull) (any, error) {
			relop, tag, mask, err := pullResRelopTagValue(p)
			if err != nil {
				return nil, err
			}
			return mapi.BitmaskRestriction{Relop: mapi.BitmaskRelop(relop), PropTag: tag, Mask: mask}, nil
		},
		mapi.ResSize: func(p *Pull) (any, error) {
			relop, tag, size, err := pullResRelopTagValue(p)
			if err != nil {
				return nil, err
			}
			return mapi.SizeRestriction{Relop: mapi.Relop(relop), PropTag: tag, Size: size}, nil
		},
		mapi.ResExist: func(p *Pull) (any, error) {
			tag, err := p.Uint32()
			if err != nil {
				return nil, err
			}
			return mapi.ExistRestriction{PropTag: mapi.PropTag(tag)}, nil
		},
		mapi.ResSub:   pullResSub,
		mapi.ResCount: pullResCountNode,
		// The recursion bottoms out here: ResNull has no payload.
		mapi.ResNull: func(*Pull) (any, error) { return nil, nil },
	}
}

// pullResChildren reads an AND/OR child list: the count, then each child.
func pullResChildren(p *Pull) (any, error) {
	n, err := pullResCount(p)
	if err != nil {
		return nil, err
	}
	if err := p.checkCount(n); err != nil {
		return nil, err
	}
	kids := make([]mapi.Restriction, n)
	for i := range kids {
		if kids[i], err = p.Restriction(); err != nil {
			return nil, err
		}
	}
	return kids, nil
}

// pullResCount reads an AND/OR child count, whose width follows FlagWCount
// (u32 set, u16 clear).
func pullResCount(p *Pull) (uint32, error) {
	if p.flags&FlagWCount != 0 {
		return p.Uint32()
	}
	v, err := p.Uint16()
	return uint32(v), err
}

// pullResPropCompare reads a PROPCOMPARE node: an operator and the two property
// tags it compares.
func pullResPropCompare(p *Pull) (any, error) {
	var pc mapi.ComparePropsRestriction
	relop, err := p.Uint8()
	if err != nil {
		return nil, err
	}
	pc.Relop = mapi.Relop(relop)
	t1, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	t2, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	pc.PropTag1, pc.PropTag2 = mapi.PropTag(t1), mapi.PropTag(t2)
	return pc, nil
}

// pullResSub reads a SUB node: the sub-object it applies to and the restriction
// evaluated against it.
func pullResSub(p *Pull) (any, error) {
	var s mapi.SubRestriction
	var err error
	if s.SubObject, err = p.Uint32(); err != nil {
		return nil, err
	}
	if s.Res, err = p.Restriction(); err != nil {
		return nil, err
	}
	return s, nil
}

// pullResCountNode reads a COUNT node: the limit and the restriction it bounds.
func pullResCountNode(p *Pull) (any, error) {
	var c mapi.CountRestriction
	var err error
	if c.Count, err = p.Uint32(); err != nil {
		return nil, err
	}
	if c.SubRes, err = p.Restriction(); err != nil {
		return nil, err
	}
	return c, nil
}

// pullResContent reads a CONTENT node: a fuzzy level, the tag it applies to, and
// the value to match.
func pullResContent(p *Pull) (any, error) {
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
	if c.PropVal, err = p.TaggedPropVal(); err != nil {
		return nil, err
	}
	return c, nil
}

// pullResProperty reads a PROPERTY node: a relational operator, the tag it
// applies to, and the value to compare against.
func pullResProperty(p *Pull) (any, error) {
	var pr mapi.PropertyRestriction
	relop, err := p.Uint8()
	if err != nil {
		return nil, err
	}
	pr.Relop = mapi.Relop(relop)
	tag, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	pr.PropTag = mapi.PropTag(tag)
	if pr.PropVal, err = p.TaggedPropVal(); err != nil {
		return nil, err
	}
	return pr, nil
}

// pullResRelopTagValue reads the shape BITMASK and SIZE share: a one-byte
// operator, the property tag, and a 32-bit operand.
func pullResRelopTagValue(p *Pull) (relop uint8, tag mapi.PropTag, value uint32, err error) {
	if relop, err = p.Uint8(); err != nil {
		return 0, 0, 0, err
	}
	raw, err := p.Uint32()
	if err != nil {
		return 0, 0, 0, err
	}
	if value, err = p.Uint32(); err != nil {
		return 0, 0, 0, err
	}
	return relop, mapi.PropTag(raw), value, nil
}

// pullResComment reads a COMMENT/ANNOTATION node: a u8 count of property values
// (at least one), those values, and an optional nested restriction.
func pullResComment(p *Pull) (any, error) {
	count, err := p.Uint8()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrFormat
	}
	c := mapi.CommentRestriction{PropVals: make([]mapi.TaggedPropVal, count)}
	for i := range c.PropVals {
		if c.PropVals[i], err = p.TaggedPropVal(); err != nil {
			return nil, err
		}
	}
	present, err := p.Uint8()
	if err != nil {
		return nil, err
	}
	if present == 0 {
		return c, nil
	}
	inner, err := p.Restriction()
	if err != nil {
		return nil, err
	}
	c.Res = &inner
	return c, nil
}
