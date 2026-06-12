package ext

import (
	"encoding/binary"
	"math"
	"unicode/utf16"

	"hermex/internal/mapi"
)

// --- integers (little-endian) ---

// Uint8 writes a single byte.
func (p *Push) Uint8(v uint8) { p.buf = append(p.buf, v) }

// Uint16 writes a 16-bit little-endian integer.
func (p *Push) Uint16(v uint16) { p.buf = binary.LittleEndian.AppendUint16(p.buf, v) }

// Uint32 writes a 32-bit little-endian integer.
func (p *Push) Uint32(v uint32) { p.buf = binary.LittleEndian.AppendUint32(p.buf, v) }

// Uint64 writes a 64-bit little-endian integer.
func (p *Push) Uint64(v uint64) { p.buf = binary.LittleEndian.AppendUint64(p.buf, v) }

// Uint8 reads a single byte.
func (p *Pull) Uint8() (uint8, error) {
	if err := p.need(1); err != nil {
		return 0, err
	}
	v := p.buf[p.off]
	p.off++
	return v, nil
}

// Uint16 reads a 16-bit little-endian integer.
func (p *Pull) Uint16() (uint16, error) {
	if err := p.need(2); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint16(p.buf[p.off:])
	p.off += 2
	return v, nil
}

// Uint32 reads a 32-bit little-endian integer.
func (p *Pull) Uint32() (uint32, error) {
	if err := p.need(4); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint32(p.buf[p.off:])
	p.off += 4
	return v, nil
}

// Uint64 reads a 64-bit little-endian integer.
func (p *Pull) Uint64() (uint64, error) {
	if err := p.need(8); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint64(p.buf[p.off:])
	p.off += 8
	return v, nil
}

// --- floats (IEEE-754 little-endian) ---

// Float32 writes a 32-bit IEEE-754 little-endian float.
func (p *Push) Float32(v float32) { p.Uint32(math.Float32bits(v)) }

// Float64 writes a 64-bit IEEE-754 little-endian float.
func (p *Push) Float64(v float64) { p.Uint64(math.Float64bits(v)) }

// Float32 reads a 32-bit IEEE-754 little-endian float.
func (p *Pull) Float32() (float32, error) {
	v, err := p.Uint32()
	return math.Float32frombits(v), err
}

// Float64 reads a 64-bit IEEE-754 little-endian float.
func (p *Pull) Float64() (float64, error) {
	v, err := p.Uint64()
	return math.Float64frombits(v), err
}

// --- bool (1 byte on the wire) ---

// Bool writes a boolean as a single 0/1 byte.
func (p *Push) Bool(v bool) {
	if v {
		p.Uint8(1)
	} else {
		p.Uint8(0)
	}
}

// Bool reads a single byte as a boolean; any value above 1 is malformed.
func (p *Pull) Bool() (bool, error) {
	v, err := p.Uint8()
	if err != nil {
		return false, err
	}
	if v > 1 {
		return false, ErrFormat
	}
	return v == 1, nil
}

// --- raw bytes ---

// Raw writes b verbatim.
func (p *Push) Raw(b []byte) { p.buf = append(p.buf, b...) }

// Raw reads exactly n bytes, returning a fresh copy.
func (p *Pull) Raw(n int) ([]byte, error) {
	if err := p.need(n); err != nil {
		return nil, err
	}
	out := make([]byte, n)
	copy(out, p.buf[p.off:p.off+n])
	p.off += n
	return out, nil
}

// --- GUID (field-wise: Data1-3 little-endian, Data4 verbatim) ---

// GUID writes a 128-bit GUID in the canonical field-wise wire layout.
func (p *Push) GUID(g mapi.GUID) {
	p.Uint32(g.Data1)
	p.Uint16(g.Data2)
	p.Uint16(g.Data3)
	p.Raw(g.Data4[:])
}

// GUID reads a 128-bit GUID.
func (p *Pull) GUID() (mapi.GUID, error) {
	var g mapi.GUID
	var err error
	if g.Data1, err = p.Uint32(); err != nil {
		return g, err
	}
	if g.Data2, err = p.Uint16(); err != nil {
		return g, err
	}
	if g.Data3, err = p.Uint16(); err != nil {
		return g, err
	}
	if err = p.need(8); err != nil {
		return g, err
	}
	copy(g.Data4[:], p.buf[p.off:p.off+8])
	p.off += 8
	return g, nil
}

// --- binary blob (length-prefixed) ---

// Bin writes a length-prefixed blob. The prefix is 32-bit when FlagWCount is
// set, otherwise 16-bit; a value too large for a 16-bit prefix is rejected.
func (p *Push) Bin(b []byte) error {
	if p.flags&FlagWCount != 0 {
		p.Uint32(uint32(len(b)))
	} else {
		if len(b) > 0xFFFF {
			return ErrFormat
		}
		p.Uint16(uint16(len(b)))
	}
	p.Raw(b)
	return nil
}

// Bin reads a length-prefixed blob using the same prefix width as Bin.
func (p *Pull) Bin() ([]byte, error) {
	var n int
	if p.flags&FlagWCount != 0 {
		v, err := p.Uint32()
		if err != nil {
			return nil, err
		}
		n = int(v)
	} else {
		v, err := p.Uint16()
		if err != nil {
			return nil, err
		}
		n = int(v)
	}
	return p.Raw(n)
}

// --- strings ---

// String8 writes a NUL-terminated byte string (code-page bytes are written
// verbatim; no transcoding).
func (p *Push) String8(s string) {
	p.Raw([]byte(s))
	p.Uint8(0)
}

// String8 reads up to and including the NUL terminator and returns the bytes
// before it.
func (p *Pull) String8() (string, error) {
	for i := p.off; i < len(p.buf); i++ {
		if p.buf[i] == 0 {
			s := string(p.buf[p.off:i])
			p.off = i + 1
			return s, nil
		}
	}
	return "", ErrFormat
}

// Unicode writes a PtUnicode string: UTF-16LE with a 00 00 terminator when
// FlagUTF16 is set, otherwise a UTF-8 NUL-terminated string.
func (p *Push) Unicode(s string) {
	if p.flags&FlagUTF16 == 0 {
		p.String8(s)
		return
	}
	for _, u := range utf16.Encode([]rune(s)) {
		p.Uint16(u)
	}
	p.Uint16(0)
}

// Unicode reads a PtUnicode string written by Unicode.
func (p *Pull) Unicode() (string, error) {
	if p.flags&FlagUTF16 == 0 {
		return p.String8()
	}
	// Scan for the aligned 00 00 terminator.
	for i := p.off; i+1 < len(p.buf); i += 2 {
		if p.buf[i] == 0 && p.buf[i+1] == 0 {
			n := (i - p.off) / 2
			units := make([]uint16, n)
			for j := range n {
				units[j] = binary.LittleEndian.Uint16(p.buf[p.off+2*j:])
			}
			p.off = i + 2
			return string(utf16.Decode(units)), nil
		}
	}
	return "", ErrFormat
}
