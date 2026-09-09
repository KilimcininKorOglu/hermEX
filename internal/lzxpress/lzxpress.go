// Package lzxpress implements the "Plain LZ77" (LZXPRESS) compression of
// [MS-XCA] sections 2.3 (compression) and 2.4 (decompression). It is the
// compression carried inside the RPC_HEADER_EXT of MAPI/HTTP and RPC/HTTP ROP
// buffers (the RHE_FLAG_COMPRESSED flag, [MS-OXCRPC] 3.1.4.1).
//
// Decompress is the deterministic, spec-defined direction and is the mandatory
// path (a client may compress its requests). Compress is used to compress
// responses above a size threshold; its output is not byte-deterministic across
// implementations (the match search is heuristic), so it is validated by
// decompressing back to the source rather than by an exact-byte comparison. The
// byte format of both directions is locked by lzxpress_test.go against vectors
// produced by an independent reference implementation.
package lzxpress

import (
	"encoding/binary"
	"errors"
)

const (
	hashBits   = 12
	hashSearch = 5
	hashMask   = uint16(1<<hashBits - 1)
	window     = 8192 // maximum match distance
)

// ErrCorrupt reports a malformed Plain-LZ77 stream (truncated, an over-long
// back-reference, or output exceeding the declared size).
var ErrCorrupt = errors.New("lzxpress: corrupt compressed data")

// Decompress expands a Plain-LZ77 stream into exactly outSize bytes (the
// uncompressed size the caller knows from the RPC_HEADER_EXT). It returns
// ErrCorrupt rather than panicking on any malformed input.
func Decompress(input []byte, outSize int) ([]byte, error) {
	if len(input) == 0 {
		return nil, nil
	}
	d := lzDecoder{in: input}
	out := make([]byte, 0, outSize)
	for {
		bit, done, err := d.nextBit()
		if err != nil {
			return nil, err
		}
		if done {
			break
		}
		if bit == 0 {
			out, err = d.literal(out, outSize)
		} else {
			out, err = d.backReference(out, outSize)
		}
		if err != nil {
			return nil, err
		}
		if len(out) >= outSize || d.inIdx >= len(input) {
			break
		}
	}
	return out, nil
}

// lzDecoder walks a Plain-LZ77 stream: the read cursor, the current indicator word
// and the nibble a previous length escape left half-used.
type lzDecoder struct {
	in        []byte
	inIdx     int
	indicator uint32
	indBit    int
	nibbleIdx int // index of a half-used length nibble, 0 = none
}

// have reports whether n more bytes are readable.
func (d *lzDecoder) have(n int) bool { return d.inIdx+n <= len(d.in) }

// nextBit returns the next indicator bit, loading a new indicator word when the
// current one is spent. done reports a trailing indicator word covering no data.
func (d *lzDecoder) nextBit() (bit uint32, done bool, err error) {
	if d.indBit == 0 {
		if !d.have(4) {
			return 0, false, ErrCorrupt
		}
		d.indicator = binary.LittleEndian.Uint32(d.in[d.inIdx:])
		d.inIdx += 4
		if d.inIdx == len(d.in) {
			// trailing indicator covering data that does not exist
			return 0, true, nil
		}
		d.indBit = 32
	}
	d.indBit--
	return (d.indicator >> uint(d.indBit)) & 1, false, nil
}

// literal copies one literal byte to the output.
func (d *lzDecoder) literal(out []byte, outSize int) ([]byte, error) {
	if !d.have(1) || len(out) >= outSize {
		return nil, ErrCorrupt
	}
	out = append(out, d.in[d.inIdx])
	d.inIdx++
	return out, nil
}

// backReference copies a match from the already-decoded output.
func (d *lzDecoder) backReference(out []byte, outSize int) ([]byte, error) {
	if !d.have(2) {
		return nil, ErrCorrupt
	}
	meta := uint32(binary.LittleEndian.Uint16(d.in[d.inIdx:]))
	d.inIdx += 2
	offset := int(meta>>3) + 1
	length := meta & 7
	if length == 7 {
		extended, err := d.extendedLength()
		if err != nil {
			return nil, err
		}
		length = extended + 7
	}
	length += 3
	for ; length > 0; length-- {
		if offset > len(out) || len(out) >= outSize {
			return nil, ErrCorrupt
		}
		out = append(out, out[len(out)-offset])
	}
	return out, nil
}

// extendedLength decodes the escape chain a three-bit length of 7 introduces: the
// shared nibble, then a byte, then the 16-bit and 32-bit fields.
func (d *lzDecoder) extendedLength() (uint32, error) {
	length, err := d.lengthNibble()
	if err != nil {
		return 0, err
	}
	if length != 15 {
		return length, nil
	}
	if !d.have(1) {
		return 0, ErrCorrupt
	}
	length = uint32(d.in[d.inIdx])
	d.inIdx++
	if length == 255 {
		if length, err = d.longLength(); err != nil {
			return 0, err
		}
	}
	return length + 15, nil
}

// lengthNibble reads the shared length nibble: the low half of a fresh byte, or the
// high half of the byte a previous escape left half-used.
func (d *lzDecoder) lengthNibble() (uint32, error) {
	if d.nibbleIdx != 0 {
		length := uint32(d.in[d.nibbleIdx] >> 4)
		d.nibbleIdx = 0
		return length, nil
	}
	if !d.have(1) {
		return 0, ErrCorrupt
	}
	d.nibbleIdx = d.inIdx
	length := uint32(d.in[d.inIdx] & 0x0f)
	d.inIdx++
	return length, nil
}

// longLength reads the 16-bit length escape and, when that reads zero, the 32-bit
// one, rejecting a value below the bias the encoder added.
func (d *lzDecoder) longLength() (uint32, error) {
	if !d.have(2) {
		return 0, ErrCorrupt
	}
	length := uint32(binary.LittleEndian.Uint16(d.in[d.inIdx:]))
	d.inIdx += 2
	if length == 0 {
		if !d.have(4) {
			return 0, ErrCorrupt
		}
		length = binary.LittleEndian.Uint32(d.in[d.inIdx:])
		d.inIdx += 4
	}
	if length < 15+7 {
		return 0, ErrCorrupt
	}
	return length - (15 + 7), nil
}

// Compress packs data into a Plain-LZ77 stream. The output decompresses back to
// data via Decompress(out, len(data)).
func Compress(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	// Plain LZ77 never expands by more than ~1/8 (one indicator bit per token);
	// 2x + a fixed margin is always sufficient and lets every position be
	// back-patched (the indicator word and the shared length nibble).
	e := lzEncoder{out: make([]byte, len(data)*2+64)}
	e.reserveIndicator()

	var hash [1 << hashBits]uint32
	for i := range hash {
		hash[i] = 0xffffffff
	}

	uPos := 0 // read cursor into data
	for uPos < len(data) {
		there, mlen := findMatch(&hash, data, uPos)
		if there < 0 {
			e.literal(data[uPos])
			uPos++
			continue
		}
		// #nosec G115 -- the match length and offset are bounded by the compressor's own window, far below the fields that carry them
		e.match(uint32(mlen-3), uint32(uPos-there-1))
		uPos += mlen
	}
	return e.flush()
}

// findMatch returns the best back-reference for the position and its length, or
// (-1, 0) when the remaining input is too short or nothing matches.
func findMatch(hash *[1 << hashBits]uint32, data []byte, uPos int) (int, int) {
	maxLen := min(len(data)-uPos, 0xffff+3)
	if maxLen < 3 {
		return -1, 0
	}
	h := threeByteHash(data[uPos:])
	// #nosec G115 -- the cursor is an index into the data being compressed, so it is non-negative and below its length
	offset := uint32(uPos)
	there, mlen := lookupMatch(hash, h, data, offset, maxLen)
	storeMatch(hash, h, offset)
	return there, mlen
}

// lzEncoder builds a Plain-LZ77 stream: the write cursor, the accumulating indicator
// word and its back-patch offset, and the nibble a previous length escape left
// half-used.
type lzEncoder struct {
	out       []byte
	pos       int    // write cursor into out
	indic     uint32 // accumulating indicator word
	indBit    int    // bits pushed into indic
	indPos    int    // out offset of the current indicator word
	nibbleIdx int    // out offset of a half-used length nibble, 0 = none
}

// reserveIndicator reserves the first indicator word, back-patched once it fills.
func (e *lzEncoder) reserveIndicator() {
	binary.LittleEndian.PutUint32(e.out[e.pos:], 0)
	e.pos += 4
}

// pushBit appends one indicator bit, flushing the word when it fills and reserving
// the next one.
func (e *lzEncoder) pushBit(bit uint32) {
	e.indic = e.indic<<1 | bit
	e.indBit++
	if e.indBit == 32 {
		binary.LittleEndian.PutUint32(e.out[e.indPos:], e.indic)
		e.indBit = 0
		e.indPos = e.pos
		e.pos += 4
	}
}

// literal writes one literal byte and its indicator bit.
func (e *lzEncoder) literal(b byte) {
	e.out[e.pos] = b
	e.pos++
	e.pushBit(0)
}

// match writes a back-reference: the meta word, the length escapes a length past
// seven needs, and the indicator bit.
func (e *lzEncoder) match(matchLen, offset uint32) {
	// #nosec G115 -- the match length and offset are bounded by the compressor's own window, far below the fields that carry them
	binary.LittleEndian.PutUint16(e.out[e.pos:], uint16(offset<<3|min(matchLen, 7)))
	e.pos += 2
	if matchLen >= 7 {
		e.writeLengthEscape(matchLen - 7)
	}
	e.pushBit(1)
}

// writeLengthEscape writes the shared nibble and, when the length overflows it, the
// byte and the long-length fields.
func (e *lzEncoder) writeLengthEscape(matchLen uint32) {
	e.writeNibble(byte(min(matchLen, 15)))
	if matchLen < 15 {
		return
	}
	matchLen -= 15
	e.out[e.pos] = byte(min(matchLen, 255))
	e.pos++
	if matchLen < 255 {
		return
	}
	e.writeLongLength(matchLen + 7 + 15)
}

// writeNibble stores a length nibble, in a fresh byte or in the high half of the byte
// a previous escape left half-used.
func (e *lzEncoder) writeNibble(v byte) {
	if e.nibbleIdx == 0 {
		e.nibbleIdx = e.pos
		e.out[e.pos] = v
		e.pos++
		return
	}
	e.out[e.nibbleIdx] |= v << 4
	e.nibbleIdx = 0
}

// writeLongLength writes the 16-bit length escape, or a zero plus the 32-bit one.
func (e *lzEncoder) writeLongLength(v uint32) {
	if v < 1<<16 {
		binary.LittleEndian.PutUint16(e.out[e.pos:], uint16(v))
		e.pos += 2
		return
	}
	binary.LittleEndian.PutUint16(e.out[e.pos:], 0)
	e.pos += 2
	binary.LittleEndian.PutUint32(e.out[e.pos:], v)
	e.pos += 4
}

// flush writes the final indicator word, padding the unused high bits with ones, and
// returns the finished stream.
func (e *lzEncoder) flush() []byte {
	if e.indBit != 0 {
		e.indic <<= uint(32 - e.indBit)
	}
	e.indic |= 0xffffffff >> uint(e.indBit)
	binary.LittleEndian.PutUint32(e.out[e.indPos:], e.indic)
	return e.out[:e.pos]
}

// threeByteHash is the [MS-XCA] plain-LZ77 match hash over three bytes.
func threeByteHash(b []byte) uint16 {
	a := uint16(b[0])
	bb := uint16(b[1]) ^ 0x2e
	c := uint16(b[2]) ^ 0x55
	ca := c - a
	d := (a+bb)<<8 ^ ca<<5 ^ (c + bb) ^ (0x0cab + a)
	return d & hashMask
}

// storeMatch records the offset of the current position in the hash table,
// probing a short window and finally evicting the most distant entry.
func storeMatch(hash *[1 << hashBits]uint32, h uint16, offset uint32) {
	o := hash[h]
	if o >= offset {
		hash[h] = offset
		return
	}
	for i := uint16(1); i < hashSearch; i++ {
		h2 := (h + i) & hashMask
		if hash[h2] >= offset {
			hash[h2] = offset
			return
		}
	}
	worstH, worstScore := h, offset-o
	for i := uint16(1); i < hashSearch; i++ {
		h2 := (h + i) & hashMask
		if score := offset - hash[h2]; score > worstScore {
			worstScore, worstH = score, h2
		}
	}
	hash[worstH] = offset
}

// lookupMatch finds the longest back-reference (>2 bytes, within the 8192-byte
// window) for the current position, returning its absolute index and length, or
// (-1, 0) when none is usable.
func lookupMatch(hash *[1 << hashBits]uint32, h uint16, data []byte, offset uint32, maxLen int) (int, int) {
	best, bestLen := -1, 0
	here := int(offset)
	for i := range uint16(hashSearch) {
		o := hash[(h+i)&hashMask]
		if o >= offset {
			break
		}
		if offset-o > window {
			continue
		}
		there := int(o)
		if bestLen > 1000 && data[there+bestLen-1] != data[best+bestLen-1] {
			continue
		}
		l := 0
		for l < maxLen && data[here+l] == data[there+l] {
			l++
		}
		if l > 2 && l > bestLen {
			bestLen, best = l, there
		}
	}
	return best, bestLen
}
