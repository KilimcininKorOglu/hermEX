// Package ext implements the Microsoft on-the-wire serialization of MAPI
// property values used inside the external Exchange protocols (ROP, NSPI, EWS):
// the little-endian, unaligned encoding of typed property values, arrays,
// GUIDs, binaries, and strings, including the multivalue/array count-width
// regimes and the UTF-16LE/UTF-8 charset gating.
//
// It reproduces only the Microsoft wire property serialization; it deliberately
// implements no internal RPC framing.
package ext
