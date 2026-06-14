package lzxpress

import (
	"bytes"
	"testing"
)

// refVectors pairs an input with the compressed bytes produced by an
// independent C implementation of the [MS-XCA] Plain-LZ77 algorithm — an oracle
// independent of this Go port. Decompress(comp, len(plain)) MUST reproduce
// plain — this is the byte-format anchor a self-round-trip alone cannot give.
// The set exercises every decode branch: bare literals + indicator fill
// (single/alphabet), a short match (repeat_abc), the >=7 length nibble and the
// >=15 extra byte (run52a), the 255 -> uint16 extension (run300a), and a
// multi-indicator-word stream with mid-stream matches (mixed).
func refVectors() []struct {
	name        string
	plain, comp []byte
} {
	return []struct {
		name        string
		plain, comp []byte
	}{
		{"empty", []byte(""), []byte("")},
		{"single", []byte("Z"), []byte("\xff\xff\xff\x7f\x5a")},
		{"alphabet", []byte("abcdefghijklmnopqrstuvwxyz"), []byte("\x3f\x00\x00\x00abcdefghijklmnopqrstuvwxyz")},
		{"repeat_abc", bytes.Repeat([]byte("abc"), 12), []byte("\xff\xff\xff\x1f\x61\x62\x63\x17\x00\x0f\x08")},
		{"run52a", bytes.Repeat([]byte("a"), 52), []byte("\xff\xff\xff\x7f\x61\x07\x00\x0f\x1a")},
		{"run300a", bytes.Repeat([]byte("a"), 300), []byte("\xff\xff\xff\x7f\x61\x07\x00\x0f\xff\x28\x01")},
		{"mixed", bytes.Repeat([]byte("Hello, hello, HELLO world! "), 8), []byte("\xff\x03\x80\x00Hello, h\x33\x00HELLO world! \xd7\x00\x0f\xa4")},
	}
}

// TestDecompressReferenceVectors locks the decode byte-format against the
// independent reference oracle across all length-encoding branches.
func TestDecompressReferenceVectors(t *testing.T) {
	for _, v := range refVectors() {
		got, err := Decompress(v.comp, len(v.plain))
		if err != nil {
			t.Errorf("%s: Decompress error: %v", v.name, err)
			continue
		}
		if !bytes.Equal(got, v.plain) {
			t.Errorf("%s: Decompress = %q, want %q", v.name, got, v.plain)
		}
	}
}

// TestCompressDeterministicVectors checks the literal+indicator encode path
// byte-for-byte against the oracle. Only the no-match inputs are deterministic
// across implementations (the match search is heuristic, so match-bearing
// outputs are validated by round-trip, not exact bytes).
func TestCompressDeterministicVectors(t *testing.T) {
	for _, name := range []string{"empty", "single", "alphabet"} {
		var v struct {
			name        string
			plain, comp []byte
		}
		for _, rv := range refVectors() {
			if rv.name == name {
				v = rv
			}
		}
		got := Compress(v.plain)
		if !bytes.Equal(got, v.comp) {
			t.Errorf("%s: Compress = %x, want %x", name, got, v.comp)
		}
	}
}

// TestRoundTrip confirms Compress emits a stream the (oracle-anchored)
// Decompress reads back exactly, and that compressible inputs actually shrink.
func TestRoundTrip(t *testing.T) {
	seq := make([]byte, 256)
	for i := range seq {
		seq[i] = byte(i)
	}
	// deterministic pseudo-random buffer (incompressible-ish)
	rnd := make([]byte, 5000)
	s := uint32(0x12345678)
	for i := range rnd {
		s = s*1664525 + 1013904223
		rnd[i] = byte(s >> 24)
	}
	inputs := map[string][]byte{
		"empty":      nil,
		"one":        {0x42},
		"two":        {0x01, 0x02},
		"alphabet":   []byte("abcdefghijklmnopqrstuvwxyz"),
		"run300a":    bytes.Repeat([]byte("a"), 300),
		"run70000a":  bytes.Repeat([]byte("a"), 70000), // exercises the >=255 uint16 length path
		"seqseq":     append(append([]byte{}, seq...), seq...),
		"mixed":      bytes.Repeat([]byte("Hello, hello, HELLO world! "), 64),
		"random5000": rnd,
	}
	for name, in := range inputs {
		comp := Compress(in)
		got, err := Decompress(comp, len(in))
		if err != nil {
			t.Errorf("%s: Decompress(Compress) error: %v", name, err)
			continue
		}
		if !bytes.Equal(got, in) {
			t.Errorf("%s: round-trip mismatch (%d in, %d comp, %d out)", name, len(in), len(comp), len(got))
		}
		if name == "run300a" || name == "run70000a" || name == "mixed" || name == "seqseq" {
			if len(comp) >= len(in) {
				t.Errorf("%s: compressed %d >= input %d (expected to shrink)", name, len(comp), len(in))
			}
		}
	}
}

// TestDecompressCorrupt confirms malformed streams return ErrCorrupt and never
// panic or over-read.
func TestDecompressCorrupt(t *testing.T) {
	cases := map[string][]byte{
		"truncated indicator": {0x00, 0x00},                         // <4 bytes for the indicator word
		"match no metadata":   {0xff, 0xff, 0xff, 0xff, 0x00},       // match flagged, only 1 byte of metadata
		"offset before start": {0xff, 0xff, 0xff, 0xff, 0x00, 0x00}, // back-ref with nothing emitted yet
		"length nibble cut":   {0xff, 0xff, 0xff, 0xff, 0x07, 0x00}, // length==7 needs a nibble byte that is absent
	}
	for name, in := range cases {
		if _, err := Decompress(in, 4096); err == nil {
			t.Errorf("%s: expected ErrCorrupt, got nil", name)
		}
	}
}
