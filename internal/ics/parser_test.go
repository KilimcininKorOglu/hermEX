package ics

import (
	"encoding/binary"
	"reflect"
	"testing"

	"hermex/internal/mapi"
)

// buildKnownStream assembles a fixed multi-element stream by hand (markers +
// encoded properties) so the fragmentation tests are independent of the
// Producer's chunking.
func buildKnownStream(t *testing.T) (data []byte, wantProps []StreamProp, wantMarkers map[int]uint32) {
	t.Helper()
	wantMarkers = map[int]uint32{}
	add := func(m uint32) {
		data = binary.LittleEndian.AppendUint32(data, m)
	}
	addProp := func(p StreamProp) {
		h, b, err := encodeProp(p)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, h...), b...)
		wantProps = append(wantProps, p)
	}
	add(markerStartMessage)
	addProp(StreamProp{Tag: tag(0x1001, mapi.PtLong), Value: int32(0x11223344)})
	addProp(StreamProp{Tag: tag(0x1002, mapi.PtUnicode), Value: "hello world"})
	addProp(StreamProp{Tag: tag(0x1003, mapi.PtBinary), Value: []byte{1, 2, 3, 4, 5, 6, 7}})
	add(markerEndMessage)
	return data, wantProps, wantMarkers
}

// feedInChunks feeds data to a fresh parser in fixed-size pieces, draining every
// complete element after each feed, and returns all items in order.
func feedInChunks(t *testing.T, data []byte, chunkSize int) []Item {
	t.Helper()
	var ps Parser
	var items []Item
	for i := 0; i < len(data); i += chunkSize {
		end := min(i+chunkSize, len(data))
		ps.Feed(data[i:end])
		for {
			it, ok, err := ps.Next()
			if err != nil {
				t.Fatalf("parser at byte %d: %v", i, err)
			}
			if !ok {
				break
			}
			items = append(items, it)
		}
	}
	return items
}

// assertStream checks the items match the known stream: marker, 3 props, marker.
func assertStream(t *testing.T, items []Item, wantProps []StreamProp) {
	t.Helper()
	if len(items) != 5 {
		t.Fatalf("got %d items, want 5", len(items))
	}
	if !items[0].IsMarker || items[0].Marker != markerStartMessage {
		t.Errorf("item 0 = %+v, want StartMessage marker", items[0])
	}
	if !items[4].IsMarker || items[4].Marker != markerEndMessage {
		t.Errorf("item 4 = %+v, want EndMessage marker", items[4])
	}
	for i, want := range wantProps {
		got := items[i+1]
		if got.Prop == nil {
			t.Errorf("item %d is not a property", i+1)
			continue
		}
		if got.Prop.Tag != want.Tag || !reflect.DeepEqual(got.Prop.Value, want.Value) {
			t.Errorf("prop %d = (%s,%#v), want (%s,%#v)", i, got.Prop.Tag, got.Prop.Value, want.Tag, want.Value)
		}
	}
}

// TestParserReassemblesByteByByte feeds the stream one byte at a time — the most
// adversarial fragmentation, splitting every multi-byte primitive (markers,
// propdefs, length prefixes) and every value body mid-element. A correct
// length-driven parser must still reconstruct every element. This path is what a
// real client's upload exercises and our own Producer never triggers.
func TestParserReassemblesByteByByte(t *testing.T) {
	data, wantProps, _ := buildKnownStream(t)
	assertStream(t, feedInChunks(t, data, 1), wantProps)
}

// TestParserReassemblesOddChunks feeds the stream in 3-byte chunks (misaligned
// to every 2- and 4-byte field and to UTF-16 code-unit boundaries).
func TestParserReassemblesOddChunks(t *testing.T) {
	data, wantProps, _ := buildKnownStream(t)
	assertStream(t, feedInChunks(t, data, 3), wantProps)
}

// TestParserWholeBuffer feeds the entire stream at once.
func TestParserWholeBuffer(t *testing.T) {
	data, wantProps, _ := buildKnownStream(t)
	assertStream(t, feedInChunks(t, data, len(data)), wantProps)
}

// TestParserSplitMarkerMidWord splits a 4-byte marker exactly 2+2 across two
// feeds and asserts it is not mistaken for an incomplete property or a wrong
// marker.
func TestParserSplitMarkerMidWord(t *testing.T) {
	var data []byte
	data = binary.LittleEndian.AppendUint32(data, markerIncrSyncStateBegin)
	var ps Parser
	ps.Feed(data[:2])
	if _, ok, err := ps.Next(); ok || err != nil {
		t.Fatalf("half a marker should yield NeedMore, got ok=%v err=%v", ok, err)
	}
	ps.Feed(data[2:])
	it, ok, err := ps.Next()
	if err != nil || !ok {
		t.Fatalf("after the rest, expected the marker: ok=%v err=%v", ok, err)
	}
	if !it.IsMarker || it.Marker != markerIncrSyncStateBegin {
		t.Fatalf("reassembled marker = %+v", it)
	}
}
