package ext

import "hermex/internal/mapi"

// Restriction writes a search restriction (p_restriction): a one-byte type tag
// followed by the payload for that type. AND/OR child counts follow FlagWCount
// (u32 set, u16 clear); COMMENT carries a u8 count (at least one); COUNT carries
// a u32. The recursion bottoms out at ResNull, which has no payload.
func (p *Push) Restriction(r mapi.Restriction) error {
	p.Uint8(uint8(r.Type))
	switch r.Type {
	case mapi.ResAnd, mapi.ResOr:
		kids, err := asType[[]mapi.Restriction](r.Value)
		if err != nil {
			return err
		}
		if p.flags&FlagWCount != 0 {
			p.Uint32(uint32(len(kids)))
		} else {
			if len(kids) > 0xFFFF {
				return ErrFormat
			}
			p.Uint16(uint16(len(kids)))
		}
		for _, k := range kids {
			if err := p.Restriction(k); err != nil {
				return err
			}
		}
		return nil
	case mapi.ResNot:
		inner, err := asType[mapi.Restriction](r.Value)
		if err != nil {
			return err
		}
		return p.Restriction(inner)
	case mapi.ResContent:
		c, err := asType[mapi.ContentRestriction](r.Value)
		if err != nil {
			return err
		}
		p.Uint32(c.FuzzyLevel)
		p.Uint32(uint32(c.PropTag))
		return p.TaggedPropVal(c.PropVal)
	case mapi.ResProperty:
		pr, err := asType[mapi.PropertyRestriction](r.Value)
		if err != nil {
			return err
		}
		p.Uint8(uint8(pr.Relop))
		p.Uint32(uint32(pr.PropTag))
		return p.TaggedPropVal(pr.PropVal)
	case mapi.ResPropCompare:
		pc, err := asType[mapi.ComparePropsRestriction](r.Value)
		if err != nil {
			return err
		}
		p.Uint8(uint8(pc.Relop))
		p.Uint32(uint32(pc.PropTag1))
		p.Uint32(uint32(pc.PropTag2))
		return nil
	case mapi.ResBitmask:
		b, err := asType[mapi.BitmaskRestriction](r.Value)
		if err != nil {
			return err
		}
		p.Uint8(uint8(b.Relop))
		p.Uint32(uint32(b.PropTag))
		p.Uint32(b.Mask)
		return nil
	case mapi.ResSize:
		s, err := asType[mapi.SizeRestriction](r.Value)
		if err != nil {
			return err
		}
		p.Uint8(uint8(s.Relop))
		p.Uint32(uint32(s.PropTag))
		p.Uint32(s.Size)
		return nil
	case mapi.ResExist:
		e, err := asType[mapi.ExistRestriction](r.Value)
		if err != nil {
			return err
		}
		p.Uint32(uint32(e.PropTag))
		return nil
	case mapi.ResSub:
		s, err := asType[mapi.SubRestriction](r.Value)
		if err != nil {
			return err
		}
		p.Uint32(s.SubObject)
		return p.Restriction(s.Res)
	case mapi.ResComment, mapi.ResAnnotation:
		c, err := asType[mapi.CommentRestriction](r.Value)
		if err != nil {
			return err
		}
		if len(c.PropVals) == 0 || len(c.PropVals) > 0xFF {
			return ErrFormat
		}
		p.Uint8(uint8(len(c.PropVals)))
		for _, pv := range c.PropVals {
			if err := p.TaggedPropVal(pv); err != nil {
				return err
			}
		}
		if c.Res != nil {
			p.Uint8(1)
			return p.Restriction(*c.Res)
		}
		p.Uint8(0)
		return nil
	case mapi.ResCount:
		c, err := asType[mapi.CountRestriction](r.Value)
		if err != nil {
			return err
		}
		p.Uint32(c.Count)
		return p.Restriction(c.SubRes)
	case mapi.ResNull:
		return nil
	default:
		return ErrFormat
	}
}

// Restriction reads a search restriction (g_restriction), mirroring the type
// dispatch and count-width rules of Push.Restriction.
func (p *Pull) Restriction() (mapi.Restriction, error) {
	rt, err := p.Uint8()
	if err != nil {
		return mapi.Restriction{}, err
	}
	r := mapi.Restriction{Type: mapi.RestrictionType(rt)}
	switch r.Type {
	case mapi.ResAnd, mapi.ResOr:
		var n int
		if p.flags&FlagWCount != 0 {
			v, err := p.Uint32()
			if err != nil {
				return r, err
			}
			n = int(v)
		} else {
			v, err := p.Uint16()
			if err != nil {
				return r, err
			}
			n = int(v)
		}
		kids := make([]mapi.Restriction, n)
		for i := range kids {
			if kids[i], err = p.Restriction(); err != nil {
				return r, err
			}
		}
		r.Value = kids
		return r, nil
	case mapi.ResNot:
		inner, err := p.Restriction()
		if err != nil {
			return r, err
		}
		r.Value = inner
		return r, nil
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
		if c.PropVal, err = p.TaggedPropVal(); err != nil {
			return r, err
		}
		r.Value = c
		return r, nil
	case mapi.ResProperty:
		var pr mapi.PropertyRestriction
		relop, err := p.Uint8()
		if err != nil {
			return r, err
		}
		pr.Relop = mapi.Relop(relop)
		tag, err := p.Uint32()
		if err != nil {
			return r, err
		}
		pr.PropTag = mapi.PropTag(tag)
		if pr.PropVal, err = p.TaggedPropVal(); err != nil {
			return r, err
		}
		r.Value = pr
		return r, nil
	case mapi.ResPropCompare:
		var pc mapi.ComparePropsRestriction
		relop, err := p.Uint8()
		if err != nil {
			return r, err
		}
		pc.Relop = mapi.Relop(relop)
		t1, err := p.Uint32()
		if err != nil {
			return r, err
		}
		t2, err := p.Uint32()
		if err != nil {
			return r, err
		}
		pc.PropTag1, pc.PropTag2 = mapi.PropTag(t1), mapi.PropTag(t2)
		r.Value = pc
		return r, nil
	case mapi.ResBitmask:
		var b mapi.BitmaskRestriction
		relop, err := p.Uint8()
		if err != nil {
			return r, err
		}
		b.Relop = mapi.BitmaskRelop(relop)
		tag, err := p.Uint32()
		if err != nil {
			return r, err
		}
		b.PropTag = mapi.PropTag(tag)
		if b.Mask, err = p.Uint32(); err != nil {
			return r, err
		}
		r.Value = b
		return r, nil
	case mapi.ResSize:
		var s mapi.SizeRestriction
		relop, err := p.Uint8()
		if err != nil {
			return r, err
		}
		s.Relop = mapi.Relop(relop)
		tag, err := p.Uint32()
		if err != nil {
			return r, err
		}
		s.PropTag = mapi.PropTag(tag)
		if s.Size, err = p.Uint32(); err != nil {
			return r, err
		}
		r.Value = s
		return r, nil
	case mapi.ResExist:
		tag, err := p.Uint32()
		if err != nil {
			return r, err
		}
		r.Value = mapi.ExistRestriction{PropTag: mapi.PropTag(tag)}
		return r, nil
	case mapi.ResSub:
		var s mapi.SubRestriction
		if s.SubObject, err = p.Uint32(); err != nil {
			return r, err
		}
		if s.Res, err = p.Restriction(); err != nil {
			return r, err
		}
		r.Value = s
		return r, nil
	case mapi.ResComment, mapi.ResAnnotation:
		count, err := p.Uint8()
		if err != nil {
			return r, err
		}
		if count == 0 {
			return r, ErrFormat
		}
		c := mapi.CommentRestriction{PropVals: make([]mapi.TaggedPropVal, count)}
		for i := range c.PropVals {
			if c.PropVals[i], err = p.TaggedPropVal(); err != nil {
				return r, err
			}
		}
		present, err := p.Uint8()
		if err != nil {
			return r, err
		}
		if present != 0 {
			inner, err := p.Restriction()
			if err != nil {
				return r, err
			}
			c.Res = &inner
		}
		r.Value = c
		return r, nil
	case mapi.ResCount:
		var c mapi.CountRestriction
		if c.Count, err = p.Uint32(); err != nil {
			return r, err
		}
		if c.SubRes, err = p.Restriction(); err != nil {
			return r, err
		}
		r.Value = c
		return r, nil
	case mapi.ResNull:
		return r, nil
	default:
		return r, ErrFormat
	}
}
