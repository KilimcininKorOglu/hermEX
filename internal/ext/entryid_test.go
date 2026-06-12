package ext

import (
	"bytes"
	"errors"
	"testing"

	"hermex/internal/mapi"
)

func sampleGUID() mapi.GUID {
	return mapi.GUID{
		Data1: 0xAABBCCDD,
		Data2: 0xEEFF,
		Data3: 0x0102,
		Data4: [8]byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48},
	}
}

func TestXIDRoundTrip(t *testing.T) {
	x := mapi.XID{GUID: sampleGUID(), LocalID: []byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60}}
	p := NewPush(0)
	if err := p.XID(x); err != nil {
		t.Fatalf("XID: %v", err)
	}
	size := 16 + len(x.LocalID)
	if p.Len() != size {
		t.Fatalf("XID length = %d, want %d", p.Len(), size)
	}
	got, err := NewPull(p.Bytes(), 0).XID(size)
	if err != nil {
		t.Fatalf("pull XID: %v", err)
	}
	if got.GUID != x.GUID || !bytes.Equal(got.LocalID, x.LocalID) {
		t.Fatalf("round-trip = %+v, want %+v", got, x)
	}
}

func TestXIDSizeBounds(t *testing.T) {
	// local id of 0 bytes -> size 16 (<17): rejected.
	if err := NewPush(0).XID(mapi.XID{GUID: sampleGUID()}); !errors.Is(err, ErrFormat) {
		t.Fatalf("empty local id err = %v, want ErrFormat", err)
	}
	// local id of 9 bytes -> size 25 (>24): rejected.
	big := mapi.XID{GUID: sampleGUID(), LocalID: make([]byte, 9)}
	if err := NewPush(0).XID(big); !errors.Is(err, ErrFormat) {
		t.Fatalf("oversize local id err = %v, want ErrFormat", err)
	}
	// pull with out-of-range size is rejected before reading.
	if _, err := NewPull(make([]byte, 24), 0).XID(16); !errors.Is(err, ErrFormat) {
		t.Fatalf("pull size 16 err = %v, want ErrFormat", err)
	}
}

func TestLongTermIDRoundTrip(t *testing.T) {
	l := mapi.LongTermID{
		GUID:          sampleGUID(),
		GlobalCounter: mapi.GlobCnt{0x21, 0x22, 0x23, 0x24, 0x25, 0x26},
		Padding:       0x3132,
	}
	p := NewPush(0)
	p.LongTermID(l)
	if p.Len() != 24 {
		t.Fatalf("LongTermID length = %d, want 24", p.Len())
	}
	got, err := NewPull(p.Bytes(), 0).LongTermID()
	if err != nil {
		t.Fatalf("pull LongTermID: %v", err)
	}
	if got != l {
		t.Fatalf("round-trip = %+v, want %+v", got, l)
	}
}

// TestFolderEntryIDVector locks the field order, widths, and — critically — the
// distinction between the flat provider uid (emitted verbatim) and the
// structured database GUID (Data1-3 byte-swapped). A field-order bug between two
// 16-byte members would survive a round-trip but not this vector.
func TestFolderEntryIDVector(t *testing.T) {
	f := mapi.FolderEntryID{
		Flags: 0x11223344,
		ProviderUID: mapi.FlatUID{
			0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
			0x18, 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F,
		},
		EIDType:      0x5566,
		FolderDBGUID: sampleGUID(),
		FolderGC:     mapi.GlobCnt{0x21, 0x22, 0x23, 0x24, 0x25, 0x26},
		Pad1:         [2]byte{0x31, 0x32},
	}
	want := []byte{
		0x44, 0x33, 0x22, 0x11, // flags LE
		0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, // provider uid verbatim
		0x18, 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F,
		0x66, 0x55, // eid_type LE
		0xDD, 0xCC, 0xBB, 0xAA, 0xFF, 0xEE, 0x02, 0x01, // db GUID Data1-3 LE
		0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, // db GUID Data4 verbatim
		0x21, 0x22, 0x23, 0x24, 0x25, 0x26, // folder gc
		0x31, 0x32, // pad1
	}
	p := NewPush(0)
	p.FolderEntryID(f)
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("FolderEntryID bytes =\n% X\nwant\n% X", p.Bytes(), want)
	}
	if p.Len() != 46 {
		t.Fatalf("FolderEntryID length = %d, want 46", p.Len())
	}
	got, err := NewPull(p.Bytes(), 0).FolderEntryID()
	if err != nil {
		t.Fatalf("pull FolderEntryID: %v", err)
	}
	if got != f {
		t.Fatalf("round-trip = %+v, want %+v", got, f)
	}
}

func TestMessageEntryIDRoundTrip(t *testing.T) {
	m := mapi.MessageEntryID{
		Flags:         0x01020304,
		ProviderUID:   mapi.FlatUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		EIDType:       0x0007,
		FolderDBGUID:  sampleGUID(),
		FolderGC:      mapi.GlobCnt{0x21, 0x22, 0x23, 0x24, 0x25, 0x26},
		Pad1:          [2]byte{0x31, 0x32},
		MessageDBGUID: mapi.GUID{Data1: 0x99887766, Data2: 0x5544, Data3: 0x3322, Data4: [8]byte{0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58}},
		MessageGC:     mapi.GlobCnt{0x61, 0x62, 0x63, 0x64, 0x65, 0x66},
		Pad2:          [2]byte{0x71, 0x72},
	}
	p := NewPush(0)
	p.MessageEntryID(m)
	if p.Len() != 70 {
		t.Fatalf("MessageEntryID length = %d, want 70", p.Len())
	}
	got, err := NewPull(p.Bytes(), 0).MessageEntryID()
	if err != nil {
		t.Fatalf("pull MessageEntryID: %v", err)
	}
	if got != m {
		t.Fatalf("round-trip = %+v, want %+v", got, m)
	}
}
