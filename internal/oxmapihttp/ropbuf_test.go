package oxmapihttp

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"hermex/internal/lzxpress"
)

// buildInner assembles a raw ROP buffer: RopSize(uint16) | rops | handle table.
func buildInner(rops []byte, handles []uint32) []byte {
	rb := binary.LittleEndian.AppendUint16(nil, uint16(len(rops)+2))
	rb = append(rb, rops...)
	for _, h := range handles {
		rb = binary.LittleEndian.AppendUint32(rb, h)
	}
	return rb
}

// wrap prepends an RPC_HEADER_EXT to a (possibly transformed) payload.
func wrap(flags, size, sizeActual uint16, payload []byte) []byte {
	out := binary.LittleEndian.AppendUint16(nil, 0) // Version
	out = binary.LittleEndian.AppendUint16(out, flags)
	out = binary.LittleEndian.AppendUint16(out, size)
	out = binary.LittleEndian.AppendUint16(out, sizeActual)
	return append(out, payload...)
}

// TestRoundTrip confirms EncodeExecute frames a buffer DecodeExecute reads back.
func TestRoundTrip(t *testing.T) {
	cases := []struct {
		rops    []byte
		handles []uint32
	}{
		{nil, nil},
		{[]byte{0x01}, nil},
		{[]byte{0xFE, 0x00, 0x00}, []uint32{0xFFFFFFFF}},
		{bytes.Repeat([]byte{0xAB}, 300), []uint32{1, 2, 3, 0xDEADBEEF}},
	}
	for i, c := range cases {
		rops, handles, err := DecodeExecute(EncodeExecute(c.rops, c.handles))
		if err != nil {
			t.Errorf("case %d: DecodeExecute error: %v", i, err)
			continue
		}
		if !bytes.Equal(rops, c.rops) && !(len(rops) == 0 && len(c.rops) == 0) {
			t.Errorf("case %d: rops = %x, want %x", i, rops, c.rops)
		}
		if !reflect.DeepEqual(normHandles(handles), normHandles(c.handles)) {
			t.Errorf("case %d: handles = %v, want %v", i, handles, c.handles)
		}
	}
}

func normHandles(h []uint32) []uint32 {
	if len(h) == 0 {
		return nil
	}
	return h
}

// TestDecodeCompressed exercises the LZXPRESS path: a compressed payload decodes
// back to the original ROP buffer.
func TestDecodeCompressed(t *testing.T) {
	rops := bytes.Repeat([]byte{0xAB}, 100)
	handles := []uint32{0x10, 0x20}
	inner := buildInner(rops, handles)
	comp := lzxpress.Compress(inner)
	if len(comp) >= len(inner) {
		t.Fatalf("test payload did not compress (%d >= %d)", len(comp), len(inner))
	}
	buf := wrap(rheFlagCompressed|rheFlagLast, uint16(len(comp)), uint16(len(inner)), comp)
	gotRops, gotHandles, err := DecodeExecute(buf)
	if err != nil {
		t.Fatalf("DecodeExecute (compressed): %v", err)
	}
	if !bytes.Equal(gotRops, rops) {
		t.Errorf("rops = %x, want %x", gotRops, rops)
	}
	if !reflect.DeepEqual(gotHandles, handles) {
		t.Errorf("handles = %v, want %v", gotHandles, handles)
	}
}

// TestDecodeXorMagic exercises the 0xA5 obfuscation path.
func TestDecodeXorMagic(t *testing.T) {
	rops := []byte{0x01, 0x02, 0x03, 0x04}
	inner := buildInner(rops, []uint32{0x99})
	ob := make([]byte, len(inner))
	for i, b := range inner {
		ob[i] = b ^ xorMagic
	}
	buf := wrap(rheFlagXorMagic|rheFlagLast, uint16(len(ob)), uint16(len(inner)), ob)
	gotRops, gotHandles, err := DecodeExecute(buf)
	if err != nil {
		t.Fatalf("DecodeExecute (xormagic): %v", err)
	}
	if !bytes.Equal(gotRops, rops) || !reflect.DeepEqual(gotHandles, []uint32{0x99}) {
		t.Errorf("xormagic decode = (%x, %v), want (%x, [153])", gotRops, gotHandles, rops)
	}
}

// TestDecodeRequiresLast confirms a header without RHE_FLAG_LAST is rejected
// (the codec supports only a single, final header).
func TestDecodeRequiresLast(t *testing.T) {
	inner := buildInner([]byte{0x01}, nil)
	buf := wrap(0, uint16(len(inner)), uint16(len(inner)), inner)
	if _, _, err := DecodeExecute(buf); err == nil {
		t.Error("expected ErrMalformed for a header without RHE_FLAG_LAST")
	}
}

// TestDecodeMalformed confirms truncated or inconsistent buffers are rejected
// without panicking.
func TestDecodeMalformed(t *testing.T) {
	inner := buildInner([]byte{0x01, 0x02}, nil)
	cases := map[string][]byte{
		"short header":     {0x00, 0x00, 0x00},
		"size past end":    wrap(rheFlagLast, 0xFFFF, uint16(len(inner)), inner),
		"ropsize past end": wrap(rheFlagLast, 4, 4, []byte{0xFF, 0xFF, 0x01, 0x02}),       // RopSize=0xFFFF > buffer
		"tail misaligned":  wrap(rheFlagLast, 5, 5, []byte{0x02, 0x00, 0x01, 0x02, 0x03}), // RopSize=2, 3 tail bytes (not %4)
	}
	for name, buf := range cases {
		if _, _, err := DecodeExecute(buf); err == nil {
			t.Errorf("%s: expected ErrMalformed, got nil", name)
		}
	}
}
