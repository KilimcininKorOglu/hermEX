package tnef

import (
	"encoding/binary"
	"errors"
)

// errShort reports a stream that ends inside a structure it announced.
var errShort = errors.New("tnef: truncated stream")

// reader walks a little-endian byte slice, failing instead of reading past the end.
type reader struct {
	b   []byte
	off int
}

// remaining is the number of unread bytes.
func (r *reader) remaining() int { return len(r.b) - r.off }

// bytes returns the next n bytes.
func (r *reader) bytes(n int) ([]byte, error) {
	if n < 0 || n > r.remaining() {
		return nil, errShort
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v, nil
}

// u8 reads one byte.
func (r *reader) u8() (byte, error) {
	b, err := r.bytes(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// u16 reads a little-endian 16-bit value.
func (r *reader) u16() (uint16, error) {
	b, err := r.bytes(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

// u32 reads a little-endian 32-bit value.
func (r *reader) u32() (uint32, error) {
	b, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

// skipPad skips the padding that brings a value of n bytes to a 4-byte boundary.
// The pad bytes are not checked: a reader must permit non-zero padding.
func (r *reader) skipPad(n int) error {
	if pad := (4 - n%4) % 4; pad > 0 {
		_, err := r.bytes(pad)
		return err
	}
	return nil
}

// count reads a 32-bit element count and refuses one the rest of the stream
// cannot hold at minSize bytes an element, so a forged count cannot drive a large
// allocation.
func (r *reader) count(minSize int) (int, error) {
	n, err := r.u32()
	if err != nil {
		return 0, err
	}
	if minSize < 1 {
		minSize = 1
	}
	if uint64(n) > uint64(r.remaining()/minSize) {
		return 0, errShort
	}
	return int(n), nil
}
