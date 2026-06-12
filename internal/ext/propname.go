package ext

import "hermex/internal/mapi"

// --- PROPERTY_NAME ---

// PropertyName writes a named property: the kind, the GUID, then
// either a 32-bit long id (MnidID) or a one-byte name length followed by the
// Unicode name (MnidString). KindNone writes nothing after the GUID. The name
// length counts the encoded name including its terminator and must fit in a
// byte.
func (p *Push) PropertyName(n mapi.PropertyName) error {
	p.Uint8(n.Kind)
	p.GUID(n.GUID)
	switch n.Kind {
	case mapi.MnidID:
		p.Uint32(n.LID)
	case mapi.MnidString:
		// The name length is the encoded byte count, computed by encoding into
		// a scratch buffer that shares this context's charset flags.
		scratch := NewPush(p.flags)
		scratch.Unicode(n.Name)
		nb := scratch.Bytes()
		if len(nb) > 0xFF {
			return ErrFormat
		}
		p.Uint8(uint8(len(nb)))
		p.Raw(nb)
	}
	return nil
}

// PropertyName reads a named property. For MnidString the leading
// length brackets the name: the Unicode read must not pass it, and the cursor is
// advanced to exactly that boundary.
func (p *Pull) PropertyName() (mapi.PropertyName, error) {
	var n mapi.PropertyName
	var err error
	if n.Kind, err = p.Uint8(); err != nil {
		return n, err
	}
	if n.GUID, err = p.GUID(); err != nil {
		return n, err
	}
	switch n.Kind {
	case mapi.MnidID:
		n.LID, err = p.Uint32()
		return n, err
	case mapi.MnidString:
		nameSize, err := p.Uint8()
		if err != nil {
			return n, err
		}
		if nameSize < 1 {
			return n, ErrFormat
		}
		end := p.off + int(nameSize)
		if end > len(p.buf) {
			return n, ErrUnderflow
		}
		if n.Name, err = p.Unicode(); err != nil {
			return n, err
		}
		if p.off > end {
			return n, ErrFormat // the name overran its declared length
		}
		p.off = end // skip any padding up to the declared length
		return n, nil
	}
	return n, nil // KindNone (or any other kind): nothing follows the GUID
}

// --- PROPNAME_ARRAY / PROPID_ARRAY ---

// PropertyNames writes a PROPNAME_ARRAY: a uint16 count followed by each named
// property.
func (p *Push) PropertyNames(names []mapi.PropertyName) error {
	if len(names) > 0xFFFF {
		return ErrFormat
	}
	p.Uint16(uint16(len(names)))
	for _, n := range names {
		if err := p.PropertyName(n); err != nil {
			return err
		}
	}
	return nil
}

// PropertyNames reads a PROPNAME_ARRAY.
func (p *Pull) PropertyNames() ([]mapi.PropertyName, error) {
	n, err := p.Uint16()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]mapi.PropertyName, n)
	for i := range out {
		if out[i], err = p.PropertyName(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PropIDs writes a PROPID_ARRAY: a uint16 count followed by each 16-bit property
// id.
func (p *Push) PropIDs(ids []uint16) error {
	if len(ids) > 0xFFFF {
		return ErrFormat
	}
	p.Uint16(uint16(len(ids)))
	for _, id := range ids {
		p.Uint16(id)
	}
	return nil
}

// PropIDs reads a PROPID_ARRAY.
func (p *Pull) PropIDs() ([]uint16, error) {
	n, err := p.Uint16()
	if err != nil {
		return nil, err
	}
	out := make([]uint16, n)
	for i := range out {
		if out[i], err = p.Uint16(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
