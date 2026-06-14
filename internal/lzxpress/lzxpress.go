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
	out := make([]byte, 0, outSize)
	var (
		inIdx     int
		indicator uint32
		indBit    int
		nibbleIdx int // index of a half-used length nibble, 0 = none
	)
	for {
		if indBit == 0 {
			if inIdx+4 > len(input) {
				return nil, ErrCorrupt
			}
			indicator = binary.LittleEndian.Uint32(input[inIdx:])
			inIdx += 4
			if inIdx == len(input) {
				// trailing indicator covering data that does not exist
				break
			}
			indBit = 32
		}
		indBit--
		if (indicator>>uint(indBit))&1 == 0 {
			// literal byte
			if inIdx+1 > len(input) || len(out) >= outSize {
				return nil, ErrCorrupt
			}
			out = append(out, input[inIdx])
			inIdx++
		} else {
			// back-reference
			if inIdx+2 > len(input) {
				return nil, ErrCorrupt
			}
			meta := uint32(binary.LittleEndian.Uint16(input[inIdx:]))
			inIdx += 2
			offset := int(meta>>3) + 1
			length := meta & 7
			if length == 7 {
				if nibbleIdx == 0 {
					if inIdx+1 > len(input) {
						return nil, ErrCorrupt
					}
					nibbleIdx = inIdx
					length = uint32(input[inIdx] & 0x0f)
					inIdx++
				} else {
					length = uint32(input[nibbleIdx] >> 4)
					nibbleIdx = 0
				}
				if length == 15 {
					if inIdx+1 > len(input) {
						return nil, ErrCorrupt
					}
					length = uint32(input[inIdx])
					inIdx++
					if length == 255 {
						if inIdx+2 > len(input) {
							return nil, ErrCorrupt
						}
						length = uint32(binary.LittleEndian.Uint16(input[inIdx:]))
						inIdx += 2
						if length == 0 {
							if inIdx+4 > len(input) {
								return nil, ErrCorrupt
							}
							length = binary.LittleEndian.Uint32(input[inIdx:])
							inIdx += 4
						}
						if length < 15+7 {
							return nil, ErrCorrupt
						}
						length -= 15 + 7
					}
					length += 15
				}
				length += 7
			}
			length += 3
			for ; length > 0; length-- {
				if offset > len(out) || len(out) >= outSize {
					return nil, ErrCorrupt
				}
				out = append(out, out[len(out)-offset])
			}
		}
		if len(out) >= outSize || inIdx >= len(input) {
			break
		}
	}
	return out, nil
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
	out := make([]byte, len(data)*2+64)

	var hash [1 << hashBits]uint32
	for i := range hash {
		hash[i] = 0xffffffff
	}

	var (
		pos       int    // write cursor into out
		indic     uint32 // accumulating indicator word
		indBit    int    // bits pushed into indic
		indPos    int    // out offset of the current indicator word
		nibbleIdx int    // out offset of a half-used length nibble, 0 = none
		uPos      int    // read cursor into data
	)
	binary.LittleEndian.PutUint32(out[pos:], 0) // reserve first indicator word
	pos += 4

	pushBit := func(bit uint32) {
		indic = indic<<1 | bit
		indBit++
		if indBit == 32 {
			binary.LittleEndian.PutUint32(out[indPos:], indic)
			indBit = 0
			indPos = pos
			pos += 4
		}
	}

	for uPos < len(data) {
		maxLen := len(data) - uPos
		if maxLen > 0xffff+3 {
			maxLen = 0xffff + 3
		}
		there, mlen := -1, 0
		if maxLen >= 3 {
			h := threeByteHash(data[uPos:])
			there, mlen = lookupMatch(&hash, h, data, uint32(uPos), maxLen)
			storeMatch(&hash, h, uint32(uPos))
		}
		if there < 0 {
			out[pos] = data[uPos]
			pos++
			uPos++
			pushBit(0)
			continue
		}
		// encode the match
		matchLen := uint32(mlen - 3)
		bestOffset := uint32(uPos - there - 1)
		binary.LittleEndian.PutUint16(out[pos:], uint16(bestOffset<<3|min(matchLen, 7)))
		pos += 2
		if matchLen >= 7 {
			matchLen -= 7
			if nibbleIdx == 0 {
				nibbleIdx = pos
				out[pos] = byte(min(matchLen, 15))
				pos++
			} else {
				out[nibbleIdx] |= byte(min(matchLen, 15) << 4)
				nibbleIdx = 0
			}
			if matchLen >= 15 {
				matchLen -= 15
				out[pos] = byte(min(matchLen, 255))
				pos++
				if matchLen >= 255 {
					matchLen += 7 + 15
					if matchLen < 1<<16 {
						binary.LittleEndian.PutUint16(out[pos:], uint16(matchLen))
						pos += 2
					} else {
						binary.LittleEndian.PutUint16(out[pos:], 0)
						pos += 2
						binary.LittleEndian.PutUint32(out[pos:], matchLen)
						pos += 4
					}
				}
			}
		}
		pushBit(1)
		uPos += mlen
	}

	// flush the final indicator word, padding the unused high bits with ones
	if indBit != 0 {
		indic <<= uint(32 - indBit)
	}
	indic |= 0xffffffff >> uint(indBit)
	binary.LittleEndian.PutUint32(out[indPos:], indic)
	return out[:pos]
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
	for i := uint16(0); i < hashSearch; i++ {
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
