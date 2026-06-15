package ics

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
)

// stubMapper is a test ReplicaMapper with a fixed replid<->GUID table.
type stubMapper struct {
	idToGUID map[uint16]mapi.GUID
	guidToID map[mapi.GUID]uint16
}

func newStubMapper(pairs map[uint16]mapi.GUID) *stubMapper {
	m := &stubMapper{idToGUID: pairs, guidToID: make(map[mapi.GUID]uint16, len(pairs))}
	for id, g := range pairs {
		m.guidToID[g] = id
	}
	return m
}

func (m *stubMapper) ToGUID(replid uint16) (mapi.GUID, bool) {
	g, ok := m.idToGUID[replid]
	return g, ok
}
func (m *stubMapper) ToID(guid mapi.GUID) (uint16, bool) { id, ok := m.guidToID[guid]; return id, ok }

var guidA = mapi.GUID{Data1: 0x11223344, Data2: 0x5566, Data3: 0x7788, Data4: [8]byte{0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00}}

// TestAppendContains verifies membership semantics: an id the client claims to
// have is found, and one in a different replica or value range is not. This is
// the question the sync delta asks of pgiven/pseen.
func TestAppendContains(t *testing.T) {
	s := NewIDSet(FormIDLoose, nil)
	s.Append(mapi.MakeEIDEx(1, 0x100))
	s.AppendRange(1, 0x200, 0x210)
	s.Append(mapi.MakeEIDEx(5, 0x900)) // a foreign replica

	for _, v := range []uint64{0x100, 0x200, 0x208, 0x210} {
		if !s.Contains(mapi.MakeEIDEx(1, v)) {
			t.Errorf("replid 1 %#x should be present", v)
		}
	}
	if !s.Contains(mapi.MakeEIDEx(5, 0x900)) {
		t.Error("replid 5 0x900 should be present")
	}
	if s.Contains(mapi.MakeEIDEx(1, 0x150)) {
		t.Error("replid 1 0x150 should be absent")
	}
	if s.Contains(mapi.MakeEIDEx(2, 0x100)) {
		t.Error("replid 2 0x100 should be absent (no such replica)")
	}
}

// TestPackedRejectsMutation verifies a packed set cannot be mutated (it is a
// serialized snapshot) and that id_packed converts to id_loose by a form flip.
func TestPackedRejectsMutation(t *testing.T) {
	s := NewIDSet(FormIDPacked, nil)
	if s.AppendRange(1, 1, 2) {
		t.Error("AppendRange must fail on a packed set")
	}
	if s.Append(mapi.MakeEIDEx(1, 1)) {
		t.Error("Append must fail on a packed set")
	}
	if !s.Convert() {
		t.Error("id_packed should convert to id_loose")
	}
	if s.Form() != FormIDLoose {
		t.Errorf("after convert form = %#x, want id_loose", uint8(s.Form()))
	}
	if !s.AppendRange(1, 1, 2) {
		t.Error("AppendRange should succeed after convert to loose")
	}
}

// TestRemove verifies a single id is removed without disturbing its neighbors.
func TestRemove(t *testing.T) {
	s := NewIDSet(FormIDLoose, nil)
	s.AppendRange(1, 1, 5)
	s.Remove(mapi.MakeEIDEx(1, 3))
	if s.Contains(mapi.MakeEIDEx(1, 3)) {
		t.Error("removed id 3 still present")
	}
	for _, v := range []uint64{1, 2, 4, 5} {
		if !s.Contains(mapi.MakeEIDEx(1, v)) {
			t.Errorf("id %d should survive the removal of 3", v)
		}
	}
}

// TestConcatenate verifies the cumulative union used by the upload state merge:
// the destination keeps its ranges and absorbs the source's.
func TestConcatenate(t *testing.T) {
	dst := NewIDSet(FormIDLoose, nil)
	dst.AppendRange(1, 1, 5)
	src := NewIDSet(FormIDLoose, nil)
	src.AppendRange(1, 10, 12)
	src.AppendRange(7, 0x70, 0x70)
	if !dst.Concatenate(src) {
		t.Fatal("Concatenate failed")
	}
	for _, c := range []struct {
		r uint16
		v uint64
	}{{1, 3}, {1, 11}, {7, 0x70}} {
		if !dst.Contains(mapi.MakeEIDEx(c.r, c.v)) {
			t.Errorf("after concatenate, replid %d %#x missing", c.r, c.v)
		}
	}
	if dst.Concatenate(NewIDSet(FormIDPacked, nil)) {
		t.Error("Concatenate must reject a packed source")
	}
}

// TestForEachRange verifies every range is enumerated with its replica id,
// including foreign replicas (replid > 1) — the delta engine's deletion pass
// depends on this to walk the client's given set.
func TestForEachRange(t *testing.T) {
	s := NewIDSet(FormIDLoose, nil)
	s.AppendRange(1, 0x10, 0x20)
	s.AppendRange(1, 0x100, 0x100)
	s.AppendRange(5, 0x5000, 0x5005)

	type tup struct {
		r      uint16
		lo, hi uint64
	}
	var got []tup
	s.ForEachRange(func(r uint16, lo, hi uint64) { got = append(got, tup{r, lo, hi}) })
	want := []tup{{1, 0x10, 0x20}, {1, 0x100, 0x100}, {5, 0x5000, 0x5005}}
	if len(got) != len(want) {
		t.Fatalf("ForEachRange yielded %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("range %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestIDLooseRoundTrip serializes a replid-keyed set and reads it back: the wire
// begins with the replica id (LE u16), and after deserialize+convert membership
// is preserved.
func TestIDLooseRoundTrip(t *testing.T) {
	s := NewIDSet(FormIDLoose, nil)
	s.AppendRange(1, 0x10, 0x20)
	s.AppendRange(5, 0x5000, 0x5005)

	wire, err := s.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	// First replica block starts with replid 1 little-endian.
	if len(wire) < 2 || wire[0] != 0x01 || wire[1] != 0x00 {
		t.Fatalf("wire should begin with replid 1 LE, got %x", wire)
	}

	back := NewIDSet(FormIDPacked, nil)
	if err := back.Deserialize(wire); err != nil {
		t.Fatal(err)
	}
	if !back.Convert() {
		t.Fatal("convert id_packed failed")
	}
	for _, c := range []struct {
		r uint16
		v uint64
	}{{1, 0x10}, {1, 0x20}, {5, 0x5000}, {5, 0x5005}} {
		if !back.Contains(mapi.MakeEIDEx(c.r, c.v)) {
			t.Errorf("round-trip lost replid %d %#x", c.r, c.v)
		}
	}
	if back.Contains(mapi.MakeEIDEx(1, 0x25)) {
		t.Error("round-trip invented replid 1 0x25")
	}
}

// TestGUIDLooseRoundTrip serializes a GUID-keyed set (the wire carries the
// 16-byte replica GUID) and reads it back. Before Convert a guid_packed set is
// not queryable; Convert re-keys each GUID block to a replid via the mapper,
// after which membership is preserved.
func TestGUIDLooseRoundTrip(t *testing.T) {
	m := newStubMapper(map[uint16]mapi.GUID{1: guidA})
	s := NewIDSet(FormGUIDLoose, m)
	s.AppendRange(1, 0x10, 0x20)

	wire, err := s.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	flat := guidA.Flat()
	if len(wire) < 16 || !bytes.Equal(wire[:16], flat[:]) {
		t.Fatalf("wire should begin with guidA flat form, got %x", wire)
	}

	back := NewIDSet(FormGUIDPacked, m)
	if err := back.Deserialize(wire); err != nil {
		t.Fatal(err)
	}
	// A guid_packed set is not queryable until converted.
	if back.Contains(mapi.MakeEIDEx(1, 0x10)) {
		t.Error("guid_packed must not be queryable before Convert")
	}
	if !back.Convert() {
		t.Fatal("convert guid_packed failed")
	}
	if back.Form() != FormGUIDLoose {
		t.Errorf("after convert form = %#x, want guid_loose", uint8(back.Form()))
	}
	for _, v := range []uint64{0x10, 0x18, 0x20} {
		if !back.Contains(mapi.MakeEIDEx(1, v)) {
			t.Errorf("round-trip lost replid 1 %#x", v)
		}
	}
}

// TestGUIDSerializeNeedsMapper verifies the GUID form refuses to serialize
// without a usable mapping, rather than emitting a zero/garbage GUID.
func TestGUIDSerializeNeedsMapper(t *testing.T) {
	s := NewIDSet(FormGUIDLoose, nil)
	s.AppendRange(1, 1, 2)
	if _, err := s.Serialize(); err == nil {
		t.Error("guid_loose serialize without a mapper should error")
	}

	s2 := NewIDSet(FormGUIDLoose, newStubMapper(map[uint16]mapi.GUID{2: guidA}))
	s2.AppendRange(1, 1, 2) // replid 1 has no mapping
	if _, err := s2.Serialize(); err == nil {
		t.Error("guid_loose serialize with an unmapped replid should error")
	}
}

// TestConvertGUIDPackedUnknownGUID verifies Convert fails (rather than guessing)
// when a serialized GUID block has no replid mapping.
func TestConvertGUIDPackedUnknownGUID(t *testing.T) {
	// Build a wire form with guidA, but give the reader a mapper that does not
	// know guidA.
	src := NewIDSet(FormGUIDLoose, newStubMapper(map[uint16]mapi.GUID{1: guidA}))
	src.AppendRange(1, 1, 2)
	wire, err := src.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	back := NewIDSet(FormGUIDPacked, newStubMapper(map[uint16]mapi.GUID{}))
	if err := back.Deserialize(wire); err != nil {
		t.Fatal(err)
	}
	if back.Convert() {
		t.Error("Convert should fail when a GUID block has no replid mapping")
	}
}
