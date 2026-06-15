// Package ndr implements the Network Data Representation (NDR, C706 / [MS-RPCE])
// marshalling primitives and the connection-oriented DCE/RPC PDU codec used by
// the RPC-over-HTTP transport ([MS-RPCH], "Outlook Anywhere").
//
// This is a distinct layer from package ext, which serializes MS-OXC property
// *values*: NDR adds natural-boundary alignment (each scalar is preceded by
// padding to its own width, measured from the stream start) and the referent-id
// pointer model. v1 is little-endian only — every Windows RPC client sends its
// data representation little-endian, and a big-endian peer is rejected at the
// PDU layer (see pdu.go).
package ndr

import (
	"encoding/binary"
	"errors"

	"hermex/internal/mapi"
)

// Errors returned by the pull/push primitives.
var (
	// ErrUnderflow is returned when a read would pass the end of the buffer.
	ErrUnderflow = errors.New("ndr: buffer underflow")
	// ErrFormat is returned when bytes are malformed for the requested type.
	ErrFormat = errors.New("ndr: malformed data")
)

// Push builds a little-endian NDR octet stream. Scalar writes self-align: each
// value is preceded by zero padding up to its natural boundary, measured from
// the start of the stream (offset 0).
type Push struct {
	buf      []byte
	ptrCount uint32 // referent-id allocator for unique pointers
}

// NewPush returns an empty Push.
func NewPush() *Push { return &Push{} }

// Bytes returns the accumulated bytes; the slice aliases the internal buffer.
func (p *Push) Bytes() []byte { return p.buf }

// Len reports how many bytes have been written (the current stream offset).
func (p *Push) Len() int { return len(p.buf) }

// Align pads with zero bytes up to an n-byte boundary.
func (p *Push) Align(n int) {
	for len(p.buf)%n != 0 {
		p.buf = append(p.buf, 0)
	}
}

// Uint8 writes a single byte (no alignment).
func (p *Push) Uint8(v uint8) { p.buf = append(p.buf, v) }

// Uint16 writes a 16-bit integer aligned to 2.
func (p *Push) Uint16(v uint16) {
	p.Align(2)
	p.buf = binary.LittleEndian.AppendUint16(p.buf, v)
}

// Uint32 writes a 32-bit integer aligned to 4.
func (p *Push) Uint32(v uint32) {
	p.Align(4)
	p.buf = binary.LittleEndian.AppendUint32(p.buf, v)
}

// Uint64 writes a 64-bit integer aligned to 8.
func (p *Push) Uint64(v uint64) {
	p.Align(8)
	p.buf = binary.LittleEndian.AppendUint64(p.buf, v)
}

// Int32 writes a signed 32-bit integer aligned to 4.
func (p *Push) Int32(v int32) { p.Uint32(uint32(v)) }

// Raw writes b verbatim with no alignment or length prefix.
func (p *Push) Raw(b []byte) { p.buf = append(p.buf, b...) }

// GUID writes a 128-bit GUID (Data1-3 little-endian, Data4 verbatim). The
// leading Uint32 aligns the whole value to 4, matching NDR.
func (p *Push) GUID(g mapi.GUID) {
	p.Uint32(g.Data1)
	p.Uint16(g.Data2)
	p.Uint16(g.Data3)
	p.Raw(g.Data4[:])
}

// UniquePtr emits a referent id: 0 for a null pointer, else a fresh non-zero id
// (ptr_count*4 | 0x00020000). The referent target, if any, is marshalled by the
// caller after the id. Returns the emitted id.
func (p *Push) UniquePtr(present bool) uint32 {
	if !present {
		p.Uint32(0)
		return 0
	}
	id := p.ptrCount*4 | 0x00020000
	p.ptrCount++
	p.Uint32(id)
	return id
}

// Pull reads a little-endian NDR octet stream. Scalar reads self-align,
// advancing past padding to the value's natural boundary.
type Pull struct {
	buf []byte
	off int
}

// NewPull returns a Pull over buf.
func NewPull(buf []byte) *Pull { return &Pull{buf: buf} }

// Offset reports the current read position.
func (p *Pull) Offset() int { return p.off }

// Remaining reports how many unread bytes are left.
func (p *Pull) Remaining() int { return len(p.buf) - p.off }

// need verifies n more bytes are available.
func (p *Pull) need(n int) error {
	if n < 0 || p.off+n > len(p.buf) {
		return ErrUnderflow
	}
	return nil
}

// Align advances past padding to an n-byte boundary.
func (p *Pull) Align(n int) {
	if r := p.off % n; r != 0 {
		p.off += n - r
	}
}

// Uint8 reads a single byte (no alignment).
func (p *Pull) Uint8() (uint8, error) {
	if err := p.need(1); err != nil {
		return 0, err
	}
	v := p.buf[p.off]
	p.off++
	return v, nil
}

// Uint16 reads a 16-bit integer aligned to 2.
func (p *Pull) Uint16() (uint16, error) {
	p.Align(2)
	if err := p.need(2); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint16(p.buf[p.off:])
	p.off += 2
	return v, nil
}

// Uint32 reads a 32-bit integer aligned to 4.
func (p *Pull) Uint32() (uint32, error) {
	p.Align(4)
	if err := p.need(4); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint32(p.buf[p.off:])
	p.off += 4
	return v, nil
}

// Uint64 reads a 64-bit integer aligned to 8.
func (p *Pull) Uint64() (uint64, error) {
	p.Align(8)
	if err := p.need(8); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint64(p.buf[p.off:])
	p.off += 8
	return v, nil
}

// Int32 reads a signed 32-bit integer aligned to 4.
func (p *Pull) Int32() (int32, error) {
	v, err := p.Uint32()
	return int32(v), err
}

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

// Rest returns a fresh copy of all unread bytes (the NDR_FLAG_REMAINING blob).
func (p *Pull) Rest() []byte {
	out := make([]byte, p.Remaining())
	copy(out, p.buf[p.off:])
	p.off = len(p.buf)
	return out
}

// GUID reads a 128-bit GUID written by Push.GUID.
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
