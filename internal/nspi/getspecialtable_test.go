package nspi

import (
	"bytes"
	"encoding/binary"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// TestPermanentEntryIDLayout pins the PermanentEntryID wire layout byte-for-byte
// ([MS-OXNSPI] 2.2.9.3): flags(0) + the address-book provider GUID + version(1) +
// display type + the NUL-terminated X500 DN. This is the identity anchor every
// address-book row's PR_ENTRYID rides on, so a drift here breaks every client
// round-trip.
func TestPermanentEntryIDLayout(t *testing.T) {
	got := permanentEntryID(dtContainer, "/")
	var want []byte
	want = binary.LittleEndian.AppendUint32(want, 0)           // flags: ENTRYID_TYPE_PERMANENT
	want = append(want, abProviderGUID[:]...)                  // provider GUID (flat)
	want = binary.LittleEndian.AppendUint32(want, 1)           // version
	want = binary.LittleEndian.AppendUint32(want, dtContainer) // display type
	want = append(want, '/', 0)                                // DN + NUL
	if !bytes.Equal(got, want) {
		t.Errorf("PermanentEntryID:\n got % x\nwant % x", got, want)
	}
	if len(got) != 28+len("/")+1 {
		t.Errorf("length = %d, want %d (28 header + dn + NUL)", len(got), 28+len("/")+1)
	}
}

// buildGetSpecialTable frames a GetSpecialTable request: flags + a STAT (carrying
// the code page) + no version + an empty auxiliary buffer.
func buildGetSpecialTable(codePage uint32) []byte {
	p := ext.NewPush(0)
	p.Uint32(0) // flags
	p.Uint8(1)  // hasStat
	pushStat(p, stat{codePage: codePage})
	p.Uint8(0)  // hasVersion = 0
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// TestGetSpecialTable proves the v1 GAL hierarchy is a single container row with
// the six grounded properties, the code page is echoed, and the row decodes
// cleanly under the address-book value encoding.
func TestGetSpecialTable(t *testing.T) {
	s := NewServer(nil, testGUID)
	resp := s.GetSpecialTable(buildGetSpecialTable(1252))

	p := ext.NewPull(resp, ext.FlagABK)
	status := mustU32(t, p, "status")
	result := mustU32(t, p, "result")
	if status != 0 || result != ecSuccess {
		t.Fatalf("status=%#x result=%#x, want 0/0", status, result)
	}
	if cp := mustU32(t, p, "codepage"); cp != 1252 {
		t.Errorf("CodePage = %d, want 1252 (echoed)", cp)
	}
	if v := mustU8(t, p, "version marker"); v != 0 {
		t.Errorf("Version marker = %#x, want 0 (absent)", v)
	}
	if hr := mustU8(t, p, "HasRows"); hr != 0xFF {
		t.Fatalf("HasRows = %#x, want 0xFF", hr)
	}
	if n := mustU32(t, p, "row count"); n != 1 {
		t.Fatalf("row count = %d, want 1", n)
	}
	row, err := p.PropertyValuesLong()
	if err != nil {
		t.Fatalf("decode container row: %v", err)
	}

	scalars := map[mapi.PropTag]any{
		mapi.PrContainerFlags:   abRecipients | abUnmodifiable,
		mapi.PrDepth:            int32(0),
		mapi.PrEmsAbContainerID: galContainerID,
		mapi.PrDisplayName:      galContainerName,
		mapi.PrEmsAbIsMaster:    false,
	}
	for tag, exp := range scalars {
		got, ok := row.Get(tag)
		if !ok {
			t.Errorf("container row missing %#x", uint32(tag))
			continue
		}
		if got != exp {
			t.Errorf("%#x = %v (%T), want %v (%T)", uint32(tag), got, got, exp, exp)
		}
	}
	// PR_ENTRYID is the container's PermanentEntryID (DT_CONTAINER, dn "/").
	eid, ok := row.Get(mapi.PrEntryID)
	if !ok {
		t.Fatal("container row missing PR_ENTRYID")
	}
	if b, isBin := eid.([]byte); !isBin || !bytes.Equal(b, permanentEntryID(dtContainer, "/")) {
		t.Errorf("PR_ENTRYID = % x, want the container PermanentEntryID", eid)
	}

	if aux := mustU32(t, p, "AuxiliaryBufferSize"); aux != 0 {
		t.Errorf("AuxiliaryBufferSize = %d, want 0", aux)
	}
	if p.Remaining() != 0 {
		t.Errorf("response has %d trailing bytes", p.Remaining())
	}
}

// mustU8/mustU32 read a field or fail the test.
func mustU8(t *testing.T, p *ext.Pull, what string) uint8 {
	t.Helper()
	v, err := p.Uint8()
	if err != nil {
		t.Fatalf("read %s: %v", what, err)
	}
	return v
}

func mustU32(t *testing.T, p *ext.Pull, what string) uint32 {
	t.Helper()
	v, err := p.Uint32()
	if err != nil {
		t.Fatalf("read %s: %v", what, err)
	}
	return v
}
