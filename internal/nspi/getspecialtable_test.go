package nspi

import (
	"bytes"
	"encoding/binary"
	"fmt"
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
	p := ext.NewPush(abkFlags)
	p.Uint32(0) // flags
	p.Uint8(1)  // hasStat
	pushStat(p, stat{codePage: codePage})
	p.Uint8(0)  // hasVersion = 0
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// TestGetSpecialTable proves the hierarchy is the GAL container followed by the
// named address lists (in registry order, each with its own container id, name,
// and a distinct EntryID), the code page is echoed, and the rows decode cleanly
// under the address-book value encoding.
func TestGetSpecialTable(t *testing.T) {
	s := NewServer(nil, testGUID)
	resp := s.GetSpecialTable(buildGetSpecialTable(1252))

	p := ext.NewPull(resp, abkFlags)
	wantEq(t, "the status", mustU32(t, p, "status"), uint32(0))
	wantEq(t, "the result", mustU32(t, p, "result"), uint32(ecSuccess))
	wantEq(t, "the echoed code page", mustU32(t, p, "codepage"), uint32(1252))
	wantEq(t, "the version marker (absent)", mustU8(t, p, "version marker"), uint8(0))
	wantEq(t, "the HasRows flag", mustU8(t, p, "HasRows"), uint8(0xFF))

	rows := readContainerRows(t, p, 1+len(addressLists))
	wantGALRow(t, rows[0])
	// Rows 1..N are the named address lists, in registry order, each carrying its
	// own container id and display name and a distinct EntryID (not the GAL's).
	for i, al := range addressLists {
		wantNamedListRow(t, rows[i+1], al)
	}

	wantEq(t, "the AuxiliaryBufferSize", mustU32(t, p, "AuxiliaryBufferSize"), uint32(0))
	wantEq(t, "the trailing bytes", p.Remaining(), 0)
}

// readContainerRows decodes the hierarchy table's rows, requiring the count.
func readContainerRows(t *testing.T, p *ext.Pull, want int) []mapi.PropertyValues {
	t.Helper()
	n := mustU32(t, p, "row count")
	if int(n) != want {
		t.Fatalf("row count = %d, want %d (GAL + %d named lists)", n, want, len(addressLists))
	}
	rows := make([]mapi.PropertyValues, n)
	for i := range rows {
		row, err := p.PropertyValuesLong()
		mustNoErr(t, "decode a container row", err)
		rows[i] = row
	}
	return rows
}

// wantGALRow checks the GAL container row: its six grounded properties, with
// PR_ENTRYID a DT_CONTAINER PermanentEntryID whose dn is "/".
func wantGALRow(t *testing.T, row mapi.PropertyValues) {
	t.Helper()
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
			t.Errorf("GAL row missing %#x", uint32(tag))
			continue
		}
		wantEq(t, fmt.Sprintf("GAL property %#x", uint32(tag)), got, exp)
	}
	galEID, ok := row.Get(mapi.PrEntryID)
	if !ok {
		t.Fatal("GAL row missing PR_ENTRYID")
	}
	if b, isBin := galEID.([]byte); !isBin || !bytes.Equal(b, permanentEntryID(dtContainer, "/")) {
		t.Errorf("GAL PR_ENTRYID = % x, want the container PermanentEntryID", galEID)
	}
}

// wantNamedListRow checks one named address list's container row.
func wantNamedListRow(t *testing.T, row mapi.PropertyValues, al addressList) {
	t.Helper()
	id, _ := row.Get(mapi.PrEmsAbContainerID)
	wantEq(t, al.name+" container id", id, any(al.id))
	name, _ := row.Get(mapi.PrDisplayName)
	wantEq(t, al.name+" display name", name, any(al.name))
	eid, ok := row.Get(mapi.PrEntryID)
	if !ok {
		t.Fatalf("%q missing PR_ENTRYID", al.name)
	}
	if b, _ := eid.([]byte); bytes.Equal(b, permanentEntryID(dtContainer, "/")) {
		t.Errorf("%q shares the GAL EntryID; each container needs a distinct one", al.name)
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
