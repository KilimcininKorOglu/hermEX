package ics

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
)

// drainAll reads every complete element a parser can yield from its current
// buffer.
func drainAll(t *testing.T, ps *Parser) []Item {
	t.Helper()
	var items []Item
	for {
		it, ok, err := ps.Next()
		if err != nil {
			t.Fatalf("parser: %v", err)
		}
		if !ok {
			return items
		}
		items = append(items, it)
	}
}

// TestProducerSingleChunk verifies a small stream drains in one ReadBuffer call
// and reassembles to the same elements.
func TestProducerSingleChunk(t *testing.T) {
	var pr Producer
	pr.WriteMarker(markerStartMessage)
	if err := pr.WriteProp(StreamProp{Tag: tag(0x1001, mapi.PtLong), Value: int32(42)}); err != nil {
		t.Fatal(err)
	}
	pr.WriteMarker(markerEndMessage)

	chunk, last := pr.ReadBuffer(4096)
	if !last {
		t.Fatal("small stream should drain in one chunk")
	}
	var ps Parser
	ps.Feed(chunk)
	items := drainAll(t, &ps)
	if len(items) != 3 || !items[0].IsMarker || items[1].Prop == nil || !items[2].IsMarker {
		t.Fatalf("reassembled %d items: %+v", len(items), items)
	}
	if items[1].Prop.Value != int32(42) {
		t.Errorf("prop value = %v", items[1].Prop.Value)
	}
}

// TestProducerNeverTearsPrimitive verifies that with only fixed-size values and
// a maxLen between one and two elements, each chunk holds whole elements (no
// primitive is split): each chunk decodes standalone with no leftover bytes.
func TestProducerNeverTearsPrimitive(t *testing.T) {
	var pr Producer
	for i := range 3 {
		if err := pr.WriteProp(StreamProp{Tag: tag(uint16(0x2000+i), mapi.PtLong), Value: int32(i)}); err != nil {
			t.Fatal(err)
		}
	}
	// Each PtLong element is 8 bytes (4 propdef + 4 value); maxLen 10 fits one.
	for pr.Pending() {
		chunk, _ := pr.ReadBuffer(10)
		if len(chunk) == 0 {
			t.Fatal("empty chunk while pending")
		}
		// The chunk must be a whole number of complete elements.
		it, n, complete, err := decodeElement(chunk)
		if err != nil || !complete || n != len(chunk) || it.Prop == nil {
			t.Fatalf("chunk %x did not hold exactly one whole element (n=%d complete=%v err=%v)", chunk, n, complete, err)
		}
	}
}

// TestProducerTearsLargeBody verifies a single value larger than maxLen is torn
// across chunks (only inside its body), and the reassembled stream is byte-exact
// and parses back to the original value.
func TestProducerTearsLargeBody(t *testing.T) {
	big := make([]byte, 5000)
	for i := range big {
		big[i] = byte(i)
	}
	var pr Producer
	if err := pr.WriteProp(StreamProp{Tag: tag(0x3001, mapi.PtBinary), Value: big}); err != nil {
		t.Fatal(err)
	}

	var reassembled []byte
	chunks := 0
	for {
		chunk, last := pr.ReadBuffer(1000) // forces tearing of the 5000-byte body
		reassembled = append(reassembled, chunk...)
		if len(chunk) > 1000 {
			t.Fatalf("chunk %d exceeded maxLen: %d bytes", chunks, len(chunk))
		}
		chunks++
		if last {
			break
		}
	}
	if chunks < 2 {
		t.Fatalf("expected the body to be torn across chunks, got %d chunk(s)", chunks)
	}
	var ps Parser
	ps.Feed(reassembled)
	items := drainAll(t, &ps)
	if len(items) != 1 || items[0].Prop == nil {
		t.Fatalf("reassembled %d items", len(items))
	}
	if got, _ := items[0].Prop.Value.([]byte); !bytes.Equal(got, big) {
		t.Errorf("large binary corrupted across chunk boundaries (len %d)", len(got))
	}
}

// TestProducerInterleavedWriteDrain verifies elements written between ReadBuffer
// calls (the download flow_list pattern) reassemble in order.
func TestProducerInterleavedWriteDrain(t *testing.T) {
	var pr Producer
	var ps Parser

	pr.WriteMarker(markerIncrSyncChg)
	chunk, _ := pr.ReadBuffer(4096)
	ps.Feed(chunk)

	_ = pr.WriteProp(StreamProp{Tag: tag(0x4001, mapi.PtUnicode), Value: "later"})
	pr.WriteMarker(markerIncrSyncEnd)
	chunk, last := pr.ReadBuffer(4096)
	if !last {
		t.Fatal("expected drain")
	}
	ps.Feed(chunk)

	items := drainAll(t, &ps)
	if len(items) != 3 || items[0].Marker != markerIncrSyncChg || items[1].Prop.Value != "later" || items[2].Marker != markerIncrSyncEnd {
		t.Fatalf("interleaved reassembly: %+v", items)
	}
}

// TestProducerDropsSvrEID verifies PT_SVREID is silently dropped (it has no
// FastTransfer form), emitting no bytes.
func TestProducerDropsSvrEID(t *testing.T) {
	var pr Producer
	if err := pr.WriteProp(StreamProp{Tag: tag(0x5001, mapi.PtSvrEID), Value: []byte{1, 2, 3}}); err != nil {
		t.Fatalf("PT_SVREID should be dropped, not error: %v", err)
	}
	if pr.Pending() {
		t.Error("PT_SVREID should emit no element")
	}
}
