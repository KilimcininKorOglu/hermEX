package ext

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

func TestPropertyNameLIDVector(t *testing.T) {
	n := mapi.PropertyName{Kind: mapi.MnidID, GUID: sampleGUID(), LID: 0x11223344}
	p := NewPush(0)
	if err := p.PropertyName(n); err != nil {
		t.Fatalf("push: %v", err)
	}
	want := []byte{
		0x00,                                           // kind MnidID
		0xDD, 0xCC, 0xBB, 0xAA, 0xFF, 0xEE, 0x02, 0x01, // GUID Data1-3 LE
		0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, // GUID Data4
		0x44, 0x33, 0x22, 0x11, // LID LE
	}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("bytes = % X, want % X", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), 0).PropertyName()
	if err != nil || got != n {
		t.Fatalf("round-trip = %+v, want %+v, err %v", got, n, err)
	}
}

func TestPropertyNameString8(t *testing.T) {
	n := mapi.PropertyName{Kind: mapi.MnidString, GUID: sampleGUID(), Name: "Keywords"}
	p := NewPush(0) // no FlagUTF16: UTF-8 NUL-terminated name
	if err := p.PropertyName(n); err != nil {
		t.Fatalf("push: %v", err)
	}
	// After kind(1)+GUID(16): name_size = len("Keywords")+1 = 9, then the bytes.
	tail := p.Bytes()[17:]
	want := append([]byte{0x09}, append([]byte("Keywords"), 0x00)...)
	if !bytes.Equal(tail, want) {
		t.Fatalf("name section = % X, want % X", tail, want)
	}
	got, err := NewPull(p.Bytes(), 0).PropertyName()
	if err != nil || got != n {
		t.Fatalf("round-trip = %+v, want %+v, err %v", got, n, err)
	}
}

func TestPropertyNameStringUTF16(t *testing.T) {
	n := mapi.PropertyName{Kind: mapi.MnidString, GUID: sampleGUID(), Name: "Keywords"}
	p := NewPush(FlagUTF16) // UTF-16LE name with 00 00 terminator
	if err := p.PropertyName(n); err != nil {
		t.Fatalf("push: %v", err)
	}
	// name_size = 8 code units * 2 + 2 (terminator) = 18.
	if got := p.Bytes()[17]; got != 18 {
		t.Fatalf("name_size = %d, want 18", got)
	}
	got, err := NewPull(p.Bytes(), FlagUTF16).PropertyName()
	if err != nil || got != n {
		t.Fatalf("round-trip = %+v, want %+v, err %v", got, n, err)
	}
}

func TestPropertyNameKindNone(t *testing.T) {
	n := mapi.PropertyName{Kind: mapi.KindNone, GUID: sampleGUID()}
	p := NewPush(0)
	if err := p.PropertyName(n); err != nil {
		t.Fatalf("push: %v", err)
	}
	if p.Len() != 17 { // kind + GUID only
		t.Fatalf("length = %d, want 17", p.Len())
	}
	got, err := NewPull(p.Bytes(), 0).PropertyName()
	if err != nil || got != n {
		t.Fatalf("round-trip = %+v, want %+v, err %v", got, n, err)
	}
}

func TestPropertyNameOversize(t *testing.T) {
	// 255 ASCII chars + NUL = 256 bytes, which cannot fit the one-byte length.
	n := mapi.PropertyName{Kind: mapi.MnidString, GUID: sampleGUID(), Name: strings.Repeat("a", 255)}
	if err := NewPush(0).PropertyName(n); !errors.Is(err, ErrFormat) {
		t.Fatalf("oversize err = %v, want ErrFormat", err)
	}
}

func TestPropertyNamesArrayRoundTrip(t *testing.T) {
	names := []mapi.PropertyName{
		{Kind: mapi.MnidID, GUID: sampleGUID(), LID: 0x8001},
		{Kind: mapi.MnidString, GUID: sampleGUID(), Name: "X-Custom"},
	}
	p := NewPush(0)
	if err := p.PropertyNames(names); err != nil {
		t.Fatalf("push: %v", err)
	}
	got, err := NewPull(p.Bytes(), 0).PropertyNames()
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(got) != len(names) || got[0] != names[0] || got[1] != names[1] {
		t.Fatalf("round-trip = %+v, want %+v", got, names)
	}
}

func TestPropIDsRoundTrip(t *testing.T) {
	ids := []uint16{0x8001, 0x8002, 0x8003}
	p := NewPush(0)
	if err := p.PropIDs(ids); err != nil {
		t.Fatalf("push: %v", err)
	}
	want := []byte{0x03, 0x00, 0x01, 0x80, 0x02, 0x80, 0x03, 0x80}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("bytes = % X, want % X", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), 0).PropIDs()
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(got) != len(ids) || got[0] != ids[0] || got[2] != ids[2] {
		t.Fatalf("round-trip = %v, want %v", got, ids)
	}
}
