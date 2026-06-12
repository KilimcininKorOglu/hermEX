package ext

import (
	"reflect"
	"testing"

	"hermex/internal/mapi"
)

func TestPCLRoundTrip(t *testing.T) {
	xids := []mapi.XID{
		{GUID: sampleGUID(), LocalID: []byte{0x0D}},                   // size 17
		{GUID: sampleGUID(), LocalID: []byte{1, 2, 3, 4, 5, 6, 7, 8}}, // size 24
	}
	p := NewPush(0)
	if err := p.PCL(xids); err != nil {
		t.Fatalf("push: %v", err)
	}
	// No outer count: the stream opens with the first entry's size byte.
	if p.Bytes()[0] != 0x11 { // 17
		t.Fatalf("first byte = %#x, want 0x11 (size of first XID)", p.Bytes()[0])
	}
	if p.Len() != (1+16+1)+(1+16+8) {
		t.Fatalf("length = %d, want %d", p.Len(), (1+16+1)+(1+16+8))
	}
	got, err := NewPull(p.Bytes(), 0).PCL()
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !reflect.DeepEqual(got, xids) {
		t.Fatalf("round-trip = %+v, want %+v", got, xids)
	}
}

func TestPCLEmpty(t *testing.T) {
	p := NewPush(0)
	if err := p.PCL(nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	if p.Len() != 0 {
		t.Fatalf("empty PCL wrote %d bytes, want 0", p.Len())
	}
	got, err := NewPull(p.Bytes(), 0).PCL()
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got != nil {
		t.Fatalf("empty PCL pulled %v, want nil", got)
	}
}
