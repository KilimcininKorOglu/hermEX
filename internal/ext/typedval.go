package ext

import (
	"fmt"

	"hermex/internal/mapi"
)

// --- TYPED_PROPVAL ---

// TypedPropVal writes a 16-bit type followed by the value of that type.
func (p *Push) TypedPropVal(t mapi.TypedPropVal) error {
	p.Uint16(uint16(t.Type))
	return p.PropValue(t.Type, t.Value)
}

// TypedPropVal reads a value that carries its own type.
func (p *Pull) TypedPropVal() (mapi.TypedPropVal, error) {
	typ, err := p.Uint16()
	if err != nil {
		return mapi.TypedPropVal{}, err
	}
	v, err := p.PropValue(mapi.PropType(typ))
	return mapi.TypedPropVal{Type: mapi.PropType(typ), Value: v}, err
}

// --- SVREID ---

// SVREID writes a server entry id. A non-nil Bin selects the binary
// form (a u16 length of cb+1, a 0 flag byte, then the bytes); otherwise the
// long-term form is written with a fixed length of 21 and a 1 flag byte.
func (p *Push) SVREID(s mapi.SVREID) error {
	if s.Bin != nil {
		if len(s.Bin)+1 > 0xFFFF {
			return ErrFormat
		}
		p.Uint16(uint16(len(s.Bin) + 1))
		p.Uint8(0)
		p.Raw(s.Bin)
		return nil
	}
	p.Uint16(21)
	p.Uint8(1)
	p.Uint64(uint64(s.FolderID))
	p.Uint64(uint64(s.MessageID))
	p.Uint32(s.Instance)
	return nil
}

// SVREID reads a server entry id. The leading u16 length counts the
// flag byte, so the binary form carries length-1 payload bytes; the long-term
// form requires length 21.
func (p *Pull) SVREID() (mapi.SVREID, error) {
	var s mapi.SVREID
	length, err := p.Uint16()
	if err != nil {
		return s, err
	}
	ours, err := p.Uint8()
	if err != nil {
		return s, err
	}
	if ours == 0 {
		n := 0
		if length > 0 {
			n = int(length) - 1
		}
		// Raw(0) returns a non-nil empty slice, preserving the binary form.
		s.Bin, err = p.Raw(n)
		return s, err
	}
	if length != 21 {
		return s, ErrFormat
	}
	folder, err := p.Uint64()
	if err != nil {
		return s, err
	}
	msg, err := p.Uint64()
	if err != nil {
		return s, err
	}
	if s.Instance, err = p.Uint32(); err != nil {
		return s, err
	}
	s.FolderID = mapi.EID(folder)
	s.MessageID = mapi.EID(msg)
	return s, nil
}

// --- FLAGGED_PROPVAL ---

// FlaggedPropVal writes a flagged property value for a column of the given
// proptag. When the column type is PtUnspecified, the with-type
// form is written: an explicit type (derived from the flag) precedes the flag
// byte. The address-book (ABK) variant of this form is added with the
// address-book serialization unit.
func (p *Push) FlaggedPropVal(tag mapi.PropTag, r mapi.FlaggedPropVal) error {
	typ := tag.Type()
	if typ == mapi.PtUnspecified {
		if p.flags&FlagABK != 0 {
			return fmt.Errorf("%w: ABK-mode flagged property value is added with the address-book unit", ErrFormat)
		}
		switch r.Flag {
		case mapi.FlaggedUnavailable:
			typ = 0
		case mapi.FlaggedError:
			typ = mapi.PtError
		default: // available: the value carries its own type
			typ = r.Type
		}
		p.Uint16(uint16(typ))
	}
	p.Uint8(r.Flag)
	switch r.Flag {
	case mapi.FlaggedAvailable:
		return p.PropValue(typ, r.Value)
	case mapi.FlaggedUnavailable:
		return nil
	case mapi.FlaggedError:
		ec, err := asType[uint32](r.Value)
		if err != nil {
			return err
		}
		p.Uint32(ec)
		return nil
	default:
		return ErrFormat
	}
}

// FlaggedPropVal reads a flagged property value for a column of the given type
// . When typ is PtUnspecified, the leading explicit type is read
// into the result's Type and used to decode an available value.
func (p *Pull) FlaggedPropVal(typ mapi.PropType) (mapi.FlaggedPropVal, error) {
	var r mapi.FlaggedPropVal
	if typ == mapi.PtUnspecified {
		t, err := p.Uint16()
		if err != nil {
			return r, err
		}
		typ = mapi.PropType(t)
		r.Type = typ
	}
	flag, err := p.Uint8()
	if err != nil {
		return r, err
	}
	r.Flag = flag
	switch flag {
	case mapi.FlaggedAvailable:
		r.Value, err = p.PropValue(typ)
		return r, err
	case mapi.FlaggedUnavailable:
		return r, nil
	case mapi.FlaggedError:
		ec, err := p.Uint32()
		if err != nil {
			return r, err
		}
		r.Value = ec
		return r, nil
	default:
		return r, ErrFormat
	}
}
