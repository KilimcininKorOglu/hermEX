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

// TestPCLRefusesMalformedEntries feeds the client-supplied predecessor change
// list of an ICS upload a size byte outside the XID range ([MS-OXCDATA] 2.2.2.3:
// 16-byte GUID plus a 1 to 8 byte LocalId, so 17 to 24) and an entry cut short.
// Each must be refused rather than read as a shorter or longer XID.
func TestPCLRefusesMalformedEntries(t *testing.T) {
	body := make([]byte, 25)
	for name, buf := range map[string][]byte{
		"size 16 (no LocalId)":  append([]byte{16}, body[:16]...),
		"size 25 (LocalId > 8)": append([]byte{25}, body...),
		"truncated entry":       append([]byte{20}, body[:10]...),
	} {
		if _, err := NewPull(buf, 0).PCL(); err == nil {
			t.Errorf("%s: a malformed PCL entry was accepted", name)
		}
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
