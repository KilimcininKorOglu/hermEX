package ext

import "hermex/internal/mapi"

// This file holds the wide-count (32-bit) array codecs and the size-prefixed
// arrays that sit beside the 16-bit property-tag and property-value arrays in
// propval.go. The count width is keyed off the array type, not the encoding
// flags: these wide-count variants and the TypedPropertyValue set use 32-bit
// counts.

// PropTagsLong writes a wide-count property-tag array: a uint32 count followed
// by each 32-bit tag. It differs from PropTags only in the count width.
func (p *Push) PropTagsLong(tags []mapi.PropTag) error {
	p.Uint32(uint32(len(tags)))
	for _, t := range tags {
		p.Uint32(uint32(t))
	}
	return nil
}

// PropTagsLong reads a wide-count property-tag array.
func (p *Pull) PropTagsLong() ([]mapi.PropTag, error) {
	n, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]mapi.PropTag, n)
	for i := range out {
		v, err := p.Uint32()
		if err != nil {
			return nil, err
		}
		out[i] = mapi.PropTag(v)
	}
	return out, nil
}

// PropertyValuesLong writes a wide-count property-value array: a uint32 count
// followed by each tagged property value. It differs from PropertyValues only
// in the count width.
func (p *Push) PropertyValuesLong(pv mapi.PropertyValues) error {
	p.Uint32(uint32(len(pv)))
	for _, tp := range pv {
		if err := p.TaggedPropVal(tp); err != nil {
			return err
		}
	}
	return nil
}

// PropertyValuesLong reads a wide-count property-value array.
func (p *Pull) PropertyValuesLong() (mapi.PropertyValues, error) {
	n, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	out := make(mapi.PropertyValues, n)
	for i := range out {
		if out[i], err = p.TaggedPropVal(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TArraySet writes a TARRAY_SET (row set): a uint32 row count followed by each
// row as a 16-bit-counted property-value array.
func (p *Push) TArraySet(rows []mapi.PropertyValues) error {
	p.Uint32(uint32(len(rows)))
	for _, row := range rows {
		if err := p.PropertyValues(row); err != nil {
			return err
		}
	}
	return nil
}

// TArraySet reads a TARRAY_SET.
func (p *Pull) TArraySet() ([]mapi.PropertyValues, error) {
	n, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]mapi.PropertyValues, n)
	for i := range out {
		if out[i], err = p.PropertyValues(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ProblemArray writes a PROBLEM_ARRAY: a uint16 count followed by each problem
// (index u16, proptag u32, error u32).
func (p *Push) ProblemArray(probs []mapi.PropertyProblem) error {
	if len(probs) > 0xFFFF {
		return ErrFormat
	}
	p.Uint16(uint16(len(probs)))
	for _, pr := range probs {
		p.Uint16(pr.Index)
		p.Uint32(uint32(pr.PropTag))
		p.Uint32(pr.Err)
	}
	return nil
}

// ProblemArray reads a PROBLEM_ARRAY.
func (p *Pull) ProblemArray() ([]mapi.PropertyProblem, error) {
	n, err := p.Uint16()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]mapi.PropertyProblem, n)
	for i := range out {
		if out[i].Index, err = p.Uint16(); err != nil {
			return nil, err
		}
		tag, err := p.Uint32()
		if err != nil {
			return nil, err
		}
		out[i].PropTag = mapi.PropTag(tag)
		if out[i].Err, err = p.Uint32(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Uint64ArrayShort writes a 16-bit-counted array of 64-bit integers. This is a
// deliberate exception to the 32-bit multivalue count rule, used where the
// protocol mandates a 16-bit count for a longlong array.
func (p *Push) Uint64ArrayShort(vs []uint64) error {
	if len(vs) > 0xFFFF {
		return ErrFormat
	}
	p.Uint16(uint16(len(vs)))
	for _, v := range vs {
		p.Uint64(v)
	}
	return nil
}

// Uint64ArrayShort reads a 16-bit-counted array of 64-bit integers, the inverse
// of Uint64ArrayShort.
func (p *Pull) Uint64ArrayShort() ([]uint64, error) {
	n, err := p.Uint16()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]uint64, n)
	for i := range out {
		if out[i], err = p.Uint64(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// EIDs writes an EID_ARRAY: a uint32 count followed by each 64-bit entry id
// (the wide-count form).
func (p *Push) EIDs(ids []mapi.EID) error {
	p.Uint32(uint32(len(ids)))
	for _, id := range ids {
		p.Uint64(uint64(id))
	}
	return nil
}

// EIDs reads an EID_ARRAY (the wide-count form).
func (p *Pull) EIDs() ([]mapi.EID, error) {
	n, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]mapi.EID, n)
	for i := range out {
		v, err := p.Uint64()
		if err != nil {
			return nil, err
		}
		out[i] = mapi.EID(v)
	}
	return out, nil
}
