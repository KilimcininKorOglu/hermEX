package ics

import (
	"bytes"
	"testing"
)

// rangesEqual compares a rangeSet's nodes to an expected slice (order matters:
// decode preserves wire order, our encoder is canonical/ascending).
func rangesEqual(got rangeSet, want []rangeNode) bool {
	if len(got.nodes) != len(want) {
		return false
	}
	for i := range want {
		if got.nodes[i] != want[i] {
			return false
		}
	}
	return true
}

// TestRangeSetMergeInvariant pins the coalescing rule that decides GLOBSET
// output shape: ranges separated by at most one value merge; a gap of two or
// more stays separate. Getting this wrong silently changes which ids a sync
// thinks the client has.
func TestRangeSetMergeInvariant(t *testing.T) {
	var adjacent rangeSet
	adjacent.insert(1, 5)
	adjacent.insert(6, 10) // touches [1,5] (6 == 5+1) -> coalesce
	if !rangesEqual(adjacent, []rangeNode{{1, 10}}) {
		t.Errorf("adjacent ranges did not merge: %v", adjacent.nodes)
	}

	var gap rangeSet
	gap.insert(1, 5)
	gap.insert(7, 10) // gap of one value (6 missing) -> stay separate
	if !rangesEqual(gap, []rangeNode{{1, 5}, {7, 10}}) {
		t.Errorf("ranges with a gap merged: %v", gap.nodes)
	}

	var overlap rangeSet
	overlap.insert(1, 5)
	overlap.insert(3, 8)
	if !rangesEqual(overlap, []rangeNode{{1, 8}}) {
		t.Errorf("overlapping ranges did not merge: %v", overlap.nodes)
	}

	var unsorted rangeSet
	unsorted.insert(100, 110)
	unsorted.insert(1, 5)
	if !rangesEqual(unsorted, []rangeNode{{1, 5}, {100, 110}}) {
		t.Errorf("insert did not keep ranges sorted: %v", unsorted.nodes)
	}
}

// TestRangeSetErase verifies a single value is removed by splitting its range.
func TestRangeSetErase(t *testing.T) {
	var rs rangeSet
	rs.insert(1, 10)
	rs.erase(5) // interior -> split
	if !rangesEqual(rs, []rangeNode{{1, 4}, {6, 10}}) {
		t.Errorf("interior erase: %v", rs.nodes)
	}
	rs = rangeSet{}
	rs.insert(1, 10)
	rs.erase(1) // low edge -> shrink
	if !rangesEqual(rs, []rangeNode{{2, 10}}) {
		t.Errorf("low-edge erase: %v", rs.nodes)
	}
	rs = rangeSet{}
	rs.insert(5, 5)
	rs.erase(5) // singleton -> empty
	if !rs.empty() {
		t.Errorf("singleton erase left %v", rs.nodes)
	}
}

// TestSerializeSingletonVector pins the single-id fast path: a depth-6 push of
// the 6 big-endian GLOBCNT bytes, then end. The decoder auto-emits the singleton
// at depth 6.
func TestSerializeSingletonVector(t *testing.T) {
	var rs rangeSet
	rs.insert(0x010203040506, 0x010203040506)
	want := []byte{0x06, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, glbEnd}
	if got := serializeGlobset(rs); !bytes.Equal(got, want) {
		t.Errorf("singleton GLOBSET\n got %x\nwant %x", got, want)
	}
}

// TestSerializeSingleRangeVector pins the single-range fast path: a range
// command with the full 6-byte low and high, then end.
func TestSerializeSingleRangeVector(t *testing.T) {
	var rs rangeSet
	rs.insert(0x102030, 0x102040)
	want := []byte{
		glbRange,
		0x00, 0x00, 0x00, 0x10, 0x20, 0x30, // low (big-endian)
		0x00, 0x00, 0x00, 0x10, 0x20, 0x40, // high
		glbEnd,
	}
	if got := serializeGlobset(rs); !bytes.Equal(got, want) {
		t.Errorf("single-range GLOBSET\n got %x\nwant %x", got, want)
	}
}

// TestSerializeMultiRangeVector pins the two-level common-prefix folding: a
// global prefix push, a singleton via depth-6 push (auto-emit/auto-pop), then a
// range that needs an inner prefix push + matching pop, then the global pop.
// This exercises the (6-depth) byte-count arithmetic that round-trip alone
// cannot catch.
func TestSerializeMultiRangeVector(t *testing.T) {
	var rs rangeSet
	rs.insert(0x10, 0x10)
	rs.insert(0xA0B0, 0xA0C0)
	want := []byte{
		0x04, 0x00, 0x00, 0x00, 0x00, // push 4 (global prefix 00 00 00 00)
		0x02, 0x00, 0x10, // push 2 -> depth 6 -> emit {0x10}, auto-pop
		0x01, 0xA0, // inner push 1 (A0)
		glbRange, 0xB0, 0xC0, // range, low/high tail (1 byte each)
		glbPop, // inner pop
		glbPop, // global pop
		glbEnd,
	}
	if got := serializeGlobset(rs); !bytes.Equal(got, want) {
		t.Errorf("multi-range GLOBSET\n got %x\nwant %x", got, want)
	}
	// Round-trip the canonical encoding back to the same ranges.
	dec, n := deserializeGlobset(want)
	if n != len(want) {
		t.Errorf("decode consumed %d of %d bytes", n, len(want))
	}
	if !rangesEqual(dec, []rangeNode{{0x10, 0x10}, {0xA0B0, 0xA0C0}}) {
		t.Errorf("multi-range round-trip: %v", dec.nodes)
	}
}

// TestDeserializeBitmaskVector pins the decode-only bitmask command, which our
// own encoder never emits, so it has no round-trip coverage. The base value is
// always present and bit i represents base+i+1. mask 0x05 (bits 0 and 2) over
// base 0x10 yields {0x10,0x11} and {0x13}; 0x12 is absent.
func TestDeserializeBitmaskVector(t *testing.T) {
	data := []byte{
		0x05, 0x00, 0x00, 0x00, 0x00, 0x00, // push 5 -> stack depth 5
		glbBitmask, 0x10, 0x05, // start 0x10, mask 0b00000101
		glbEnd,
	}
	rs, n := deserializeGlobset(data)
	if n != len(data) {
		t.Fatalf("decode consumed %d of %d bytes", n, len(data))
	}
	if !rangesEqual(rs, []rangeNode{{0x10, 0x11}, {0x13, 0x13}}) {
		t.Fatalf("bitmask decode ranges: %v", rs.nodes)
	}
	for _, v := range []uint64{0x10, 0x11, 0x13} {
		if !rs.contains(v) {
			t.Errorf("bitmask: %#x should be present", v)
		}
	}
	for _, v := range []uint64{0x0F, 0x12, 0x14} {
		if rs.contains(v) {
			t.Errorf("bitmask: %#x should be absent", v)
		}
	}
}

// TestGlobsetRoundTripDense round-trips a denser coalesced set through encode +
// decode and asserts membership is preserved.
func TestGlobsetRoundTripDense(t *testing.T) {
	var rs rangeSet
	rs.insert(1, 3)
	rs.insert(100, 100)
	rs.insert(0x1000, 0x2000)
	rs.insert(0xFFFFFFFFFFFF, 0xFFFFFFFFFFFF) // max 48-bit value

	enc := serializeGlobset(rs)
	if enc[len(enc)-1] != glbEnd {
		t.Fatalf("encoding not terminated by end: %x", enc)
	}
	dec, n := deserializeGlobset(enc)
	if n != len(enc) {
		t.Fatalf("decode consumed %d of %d bytes", n, len(enc))
	}
	for _, v := range []uint64{1, 2, 3, 100, 0x1000, 0x1ABC, 0x2000, 0xFFFFFFFFFFFF} {
		if !dec.contains(v) {
			t.Errorf("round-trip: %#x should be present", v)
		}
	}
	for _, v := range []uint64{0, 4, 99, 101, 0xFFF, 0x2001} {
		if dec.contains(v) {
			t.Errorf("round-trip: %#x should be absent", v)
		}
	}
}
