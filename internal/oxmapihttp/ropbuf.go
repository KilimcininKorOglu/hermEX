// Package oxmapihttp is the wire codec between the MAPI/HTTP transport and the
// ROP layer: the RPC_HEADER_EXT envelope ([MS-OXCRPC] 2.2.2.1) and the ROP
// buffer ([MS-OXCROPS] 2.2.1) carried inside an EMSMDB Execute request/response.
//
// A decoded buffer yields the raw ROP-command region and the server-object
// handle table; parsing individual ROPs is the ROP layer's job. The envelope
// handles the LZXPRESS compression (internal/lzxpress) and the 0xA5 XorMagic
// obfuscation that a client may apply to its request payload.
package oxmapihttp

import (
	"encoding/binary"
	"errors"

	"hermex/internal/lzxpress"
)

// RPC_HEADER_EXT flags ([MS-OXCRPC] 2.2.2.1).
const (
	rheFlagCompressed = 0x0001
	rheFlagXorMagic   = 0x0002
	rheFlagLast       = 0x0004
	xorMagic          = 0xA5
	// maxROPBuffer bounds a decompressed ROP buffer, matching the reference's
	// 32 KiB working buffer; SizeActual is a uint16 so this is never exceeded.
	maxROPBuffer = 0x8000
)

// ErrMalformed reports a truncated or inconsistent Execute buffer.
var ErrMalformed = errors.New("oxmapihttp: malformed ROP buffer")

// DecodeExecute parses the opaque Execute request buffer (the RPC_HEADER_EXT
// envelope + payload) into the raw ROP-command region and the server-object
// handle table. The payload is deobfuscated (XorMagic) then decompressed
// (LZXPRESS) as the header flags direct.
func DecodeExecute(in []byte) (rops []byte, handles []uint32, err error) {
	rb, err := ropBuffer(in)
	if err != nil {
		return nil, nil, err
	}
	return splitROPBuffer(rb)
}

// ropBuffer unwraps the RPC_HEADER_EXT envelope: it validates the header, then
// deobfuscates (XorMagic) and decompresses (LZXPRESS) the payload as the flags
// direct, returning the ROP buffer the client sent.
func ropBuffer(in []byte) ([]byte, error) {
	flags, payload, sizeActual, err := header(in)
	if err != nil {
		return nil, err
	}
	payload = deobfuscate(payload, flags)
	if flags&rheFlagCompressed != 0 {
		dec, derr := lzxpress.Decompress(payload, sizeActual)
		if derr != nil || len(dec) < sizeActual {
			return nil, ErrMalformed
		}
		return dec[:sizeActual], nil
	}
	if sizeActual > len(payload) {
		return nil, ErrMalformed
	}
	return payload[:sizeActual], nil
}

// header validates the RPC_HEADER_EXT and returns its flags, the payload it frames,
// and the decompressed size the payload expands to.
func header(in []byte) (flags uint16, payload []byte, sizeActual int, err error) {
	if len(in) < 8 {
		return 0, nil, 0, ErrMalformed
	}
	flags = binary.LittleEndian.Uint16(in[2:])
	size := int(binary.LittleEndian.Uint16(in[4:]))
	sizeActual = int(binary.LittleEndian.Uint16(in[6:]))
	if flags&rheFlagLast == 0 {
		return 0, nil, 0, ErrMalformed // only a single, final header is supported
	}
	if size == 0 || 8+size > len(in) {
		return 0, nil, 0, ErrMalformed
	}
	if sizeActual == 0 || sizeActual > maxROPBuffer {
		return 0, nil, 0, ErrMalformed
	}
	return flags, in[8 : 8+size], sizeActual, nil
}

// deobfuscate undoes the XorMagic obfuscation when the flags declare it, and
// otherwise returns the payload as it stands.
func deobfuscate(payload []byte, flags uint16) []byte {
	if flags&rheFlagXorMagic == 0 {
		return payload
	}
	de := make([]byte, len(payload))
	for i, b := range payload {
		de[i] = b ^ xorMagic
	}
	return de
}

// splitROPBuffer splits the ROP buffer into its two regions:
// RopSize(uint16, inclusive of itself) | ROP commands | handle table.
func splitROPBuffer(rb []byte) (rops []byte, handles []uint32, err error) {
	if len(rb) < 2 {
		return nil, nil, ErrMalformed
	}
	ropSize := int(binary.LittleEndian.Uint16(rb))
	if ropSize < 2 || ropSize > len(rb) {
		return nil, nil, ErrMalformed
	}
	rops = rb[2:ropSize]
	tail := rb[ropSize:]
	if len(tail)%4 != 0 {
		return nil, nil, ErrMalformed
	}
	handles = make([]uint32, len(tail)/4)
	for i := range handles {
		handles[i] = binary.LittleEndian.Uint32(tail[i*4:])
	}
	return rops, handles, nil
}

// EncodeExecute frames a ROP response buffer (the ROP-command region + the
// server-object handle table) into an Execute response payload: the ROP buffer
// wrapped in an uncompressed, final RPC_HEADER_EXT. v1 responses are small, so
// they ship uncompressed (the COMPRESSED/XorMagic flags are cleared); the
// client gates on the flags, so an uncompressed buffer is always valid.
func EncodeExecute(rops []byte, handles []uint32) []byte {
	// #nosec G115 -- the length is bounded before it reaches the field, by the range check above it or by the 16-bit prefix the bytes were read with
	rb := binary.LittleEndian.AppendUint16(nil, uint16(len(rops)+2)) // RopSize
	rb = append(rb, rops...)
	for _, h := range handles {
		rb = binary.LittleEndian.AppendUint32(rb, h)
	}
	// #nosec G115 -- the length is bounded before it reaches the field, by the range check above it or by the 16-bit prefix the bytes were read with
	sizeActual := uint16(len(rb))
	out := binary.LittleEndian.AppendUint16(nil, 0)          // Version
	out = binary.LittleEndian.AppendUint16(out, rheFlagLast) // Flags
	out = binary.LittleEndian.AppendUint16(out, sizeActual)  // Size
	out = binary.LittleEndian.AppendUint16(out, sizeActual)  // SizeActual
	return append(out, rb...)
}
