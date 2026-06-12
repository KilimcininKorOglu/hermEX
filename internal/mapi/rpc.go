package mapi

// RPC header extension flags (MS-OXCRPC §2.2.2.1).
const (
	// RHEFlagCompressed marks the payload as compressed (Size < SizeActual).
	RHEFlagCompressed uint16 = 0x0001
	// RHEFlagXorMagic marks the payload as obfuscated with the Xor pattern.
	RHEFlagXorMagic uint16 = 0x0002
	// RHEFlagLast marks the final segment of a multi-segment payload.
	RHEFlagLast uint16 = 0x0004
)

// RPCHeaderExt is the 8-byte header prefixing each payload segment in the
// MAPI/HTTP and RPC/HTTP transports (MS-OXCRPC §2.2.2.1): a version, flags, the
// on-the-wire payload size, and the payload size after decompression.
type RPCHeaderExt struct {
	Version    uint16
	Flags      uint16
	Size       uint16
	SizeActual uint16
}
