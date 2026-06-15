package ics

import (
	"testing"

	"hermex/internal/mapi"
)

// serializedIDSet builds a guid_loose idset over replid 1 with one range and
// returns its wire bytes (a state meta-tag's payload).
func serializedIDSet(t *testing.T, m ReplicaMapper, lo, hi uint64) []byte {
	t.Helper()
	set := NewIDSet(FormGUIDLoose, m)
	set.AppendRange(1, lo, hi)
	b, err := set.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func stateTags(props []StreamProp) map[uint32]bool {
	tags := map[uint32]bool{}
	for _, p := range props {
		tags[uint32(p.Tag)] = true
	}
	return tags
}

// TestStateRoundTrip seeds every idset of a download state, serializes it, then
// reads it back into a fresh state and asserts membership is preserved — the
// path by which a client's prior state and a server's new state are exchanged.
func TestStateRoundTrip(t *testing.T) {
	m := newStubMapper(map[uint16]mapi.GUID{1: guidA})
	s := NewState(ContentsDown, m)
	s.Given().AppendRange(1, 0x10, 0x20)
	s.Seen().AppendRange(1, 0x100, 0x110)
	s.SeenFAI().AppendRange(1, 0x200, 0x200)
	s.Read().AppendRange(1, 0x300, 0x305)

	props, err := s.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 4 {
		t.Fatalf("ContentsDown should serialize 4 meta-tags, got %d", len(props))
	}

	back := NewState(ContentsDown, m)
	for _, p := range props {
		if err := back.AppendIDSet(uint32(p.Tag), p.Value.([]byte)); err != nil {
			t.Fatalf("AppendIDSet %s: %v", p.Tag, err)
		}
	}
	check := func(name string, set *IDSet, v uint64) {
		if !set.Contains(mapi.MakeEIDEx(1, v)) {
			t.Errorf("%s lost %#x across round-trip", name, v)
		}
	}
	check("given", back.Given(), 0x18)
	check("seen", back.Seen(), 0x108)
	check("seenFAI", back.SeenFAI(), 0x200)
	check("read", back.Read(), 0x303)
}

// TestStateUploadCumulativeMerge verifies that on an upload state successive
// CnsetSeen uploads UNION rather than replace — the cached-mode client sends
// incremental state and the server must accumulate it.
func TestStateUploadCumulativeMerge(t *testing.T) {
	m := newStubMapper(map[uint16]mapi.GUID{1: guidA})
	s := NewState(ContentsUp, m)
	if err := s.AppendIDSet(metaTagCnsetSeen, serializedIDSet(t, m, 0x10, 0x20)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendIDSet(metaTagCnsetSeen, serializedIDSet(t, m, 0x100, 0x110)); err != nil {
		t.Fatal(err)
	}
	if !s.Seen().Contains(mapi.MakeEIDEx(1, 0x18)) {
		t.Error("cumulative merge dropped the first upload's range")
	}
	if !s.Seen().Contains(mapi.MakeEIDEx(1, 0x108)) {
		t.Error("cumulative merge dropped the second upload's range")
	}
}

// TestStateGivenReplaced verifies the given set is REPLACED (not merged) across
// uploads, unlike the seen sets.
func TestStateGivenReplaced(t *testing.T) {
	m := newStubMapper(map[uint16]mapi.GUID{1: guidA})
	s := NewState(ContentsUp, m)
	if err := s.AppendIDSet(metaTagIdsetGiven1, serializedIDSet(t, m, 0x10, 0x20)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendIDSet(metaTagIdsetGiven1, serializedIDSet(t, m, 0x100, 0x110)); err != nil {
		t.Fatal(err)
	}
	if s.Given().Contains(mapi.MakeEIDEx(1, 0x18)) {
		t.Error("given should be replaced, but the first upload's range survived")
	}
	if !s.Given().Contains(mapi.MakeEIDEx(1, 0x108)) {
		t.Error("given lost the latest upload's range")
	}
}

// TestStateSerializeTypeGating verifies each type emits only its applicable
// meta-tags.
func TestStateSerializeTypeGating(t *testing.T) {
	m := newStubMapper(map[uint16]mapi.GUID{1: guidA})

	hd := NewState(HierarchyDown, m)
	hd.Given().AppendRange(1, 1, 1)
	tags := stateTags(mustSerialize(t, hd))
	if !tags[metaTagIdsetGiven1] || !tags[metaTagCnsetSeen] {
		t.Error("HierarchyDown must emit given + seen")
	}
	if tags[metaTagCnsetSeenFAI] || tags[metaTagCnsetRead] {
		t.Error("HierarchyDown must not emit seenFAI/read")
	}

	hu := NewState(HierarchyUp, m)
	tags = stateTags(mustSerialize(t, hu))
	if len(tags) != 1 || !tags[metaTagCnsetSeen] {
		t.Errorf("HierarchyUp must emit only seen, got %v", tags)
	}

	cd := NewState(ContentsDown, m)
	tags = stateTags(mustSerialize(t, cd))
	for _, want := range []uint32{metaTagIdsetGiven1, metaTagCnsetSeen, metaTagCnsetSeenFAI, metaTagCnsetRead} {
		if !tags[want] {
			t.Errorf("ContentsDown missing meta-tag %#x", want)
		}
	}
}

func mustSerialize(t *testing.T, s *State) []StreamProp {
	t.Helper()
	props, err := s.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return props
}
