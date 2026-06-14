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
	if len(in) < 8 {
		return nil, nil, ErrMalformed
	}
	flags := binary.LittleEndian.Uint16(in[2:])
	size := int(binary.LittleEndian.Uint16(in[4:]))
	sizeActual := int(binary.LittleEndian.Uint16(in[6:]))
	if flags&rheFlagLast == 0 {
		return nil, nil, ErrMalformed // only a single, final header is supported
	}
	if size == 0 || 8+size > len(in) {
		return nil, nil, ErrMalformed
	}
	if sizeActual == 0 || sizeActual > maxROPBuffer {
		return nil, nil, ErrMalformed
	}
	payload := in[8 : 8+size]

	if flags&rheFlagXorMagic != 0 {
		de := make([]byte, len(payload))
		for i, b := range payload {
			de[i] = b ^ xorMagic
		}
		payload = de
	}

	var rb []byte
	if flags&rheFlagCompressed != 0 {
		dec, derr := lzxpress.Decompress(payload, sizeActual)
		if derr != nil || len(dec) < sizeActual {
			return nil, nil, ErrMalformed
		}
		rb = dec[:sizeActual]
	} else {
		if sizeActual > len(payload) {
			return nil, nil, ErrMalformed
		}
		rb = payload[:sizeActual]
	}

	// ROP buffer: RopSize(uint16, inclusive of itself) | ROP commands | handle table.
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
	rb := binary.LittleEndian.AppendUint16(nil, uint16(len(rops)+2)) // RopSize
	rb = append(rb, rops...)
	for _, h := range handles {
		rb = binary.LittleEndian.AppendUint32(rb, h)
	}
	sizeActual := uint16(len(rb))
	out := binary.LittleEndian.AppendUint16(nil, 0)          // Version
	out = binary.LittleEndian.AppendUint16(out, rheFlagLast) // Flags
	out = binary.LittleEndian.AppendUint16(out, sizeActual)  // Size
	out = binary.LittleEndian.AppendUint16(out, sizeActual)  // SizeActual
	return append(out, rb...)
}
