package ext

import "hermex/internal/mapi"

// RPCHeaderExt writes the 8-byte RPC header extension: version, flags, size, and
// decompressed size, each a 16-bit little-endian field. It is the inverse of the
// RPCHeaderExt reader.
func (p *Push) RPCHeaderExt(h mapi.RPCHeaderExt) {
	p.Uint16(h.Version)
	p.Uint16(h.Flags)
	p.Uint16(h.Size)
	p.Uint16(h.SizeActual)
}

// RPCHeaderExt reads the 8-byte RPC header extension.
func (p *Pull) RPCHeaderExt() (mapi.RPCHeaderExt, error) {
	var h mapi.RPCHeaderExt
	var err error
	if h.Version, err = p.Uint16(); err != nil {
		return h, err
	}
	if h.Flags, err = p.Uint16(); err != nil {
		return h, err
	}
	if h.Size, err = p.Uint16(); err != nil {
		return h, err
	}
	h.SizeActual, err = p.Uint16()
	return h, err
}
