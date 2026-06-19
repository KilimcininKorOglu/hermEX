package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildGetPermissionsTable builds a RopGetPermissionsTable request: RopId, LogonId,
// InputHandleIndex (the folder), OutputHandleIndex (the table), TableFlags.
func buildGetPermissionsTable(inIdx, outIdx, tableFlags uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetPermissionsTable)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(tableFlags)
	return b.Bytes()
}

// permissionsTableHandle parses a RopGetPermissionsTable response and asserts the
// MS-OXCPERM bare-head contract: exactly RopId + HandleIndex + ReturnValue, with NO
// trailing RowCount (a phantom RowCount would desync the whole ROP batch parse).
func permissionsTableHandle(t *testing.T, resp []byte) {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropGetPermissionsTable {
		t.Fatalf("RopId = %#x, want GetPermissionsTable", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetPermissionsTable ReturnValue = %#x", ec)
	}
	if p.Remaining() != 0 {
		t.Fatalf("GetPermissionsTable response has %d trailing bytes; the contract is a bare head (no RowCount)", p.Remaining())
	}
}

// permRows opens a permission table on the given folder and reads its rows, returning
// them indexed by wire member id. It walks Logon -> OpenFolder -> GetPermissionsTable
// -> SetColumns -> QueryRows, the path an Outlook permissions dialog drives.
func permRows(t *testing.T, dir string, folderFID uint64, includeFreeBusy bool) map[int64]mapi.PropertyValues {
	t.Helper()
	sess := NewSession(dir, nil, "")
	t.Cleanup(sess.Close)

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, folderFID))), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]

	var flags uint8
	if includeFreeBusy {
		flags = permTableIncludeFreeBusy
	}
	gpt, h := sess.Dispatch(buildGetPermissionsTable(0, 1, flags), []uint32{folderH, 0xFFFFFFFF})
	permissionsTableHandle(t, gpt)
	tableH := h[1]

	cols := []mapi.PropTag{mapi.PrMemberID, mapi.PrMemberName, mapi.PrMemberRights}
	sess.Dispatch(buildSetColumns(0, cols), []uint32{tableH})
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 64), []uint32{tableH})

	_, rows := queryRowsResponse(t, qr, cols)
	out := make(map[int64]mapi.PropertyValues, len(rows))
	for _, r := range rows {
		id, _ := r.Get(mapi.PrMemberID)
		out[id.(int64)] = r
	}
	return out
}

// TestGetPermissionsTableSeededDefault drives the full read path against the seeded
// Calendar folder: its stored "default" free/busy row must surface as wire member id
// 0 with its name and rights, and the always-present anonymous member is synthesized.
// The response head is the bare 6-byte form (verified by permissionsTableHandle).
func TestGetPermissionsTableSeededDefault(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	rows := permRows(t, dir, mapi.PrivateFIDCalendar, true)
	def, ok := rows[mapi.MemberIDDefault]
	if !ok {
		t.Fatal("no Default (member id 0) row in the seeded Calendar permissions")
	}
	if name, _ := def.Get(mapi.PrMemberName); name != "default" {
		t.Errorf("Default member name = %v, want \"default\"", name)
	}
	// With IncludeFreeBusy the seeded FreeBusySimple|Visible (0xC00) is kept whole.
	if r, _ := def.Get(mapi.PrMemberRights); r != int32(0xC00) {
		t.Errorf("Default rights = %v, want 0xC00 (FreeBusySimple|Visible)", r)
	}
	if _, ok := rows[mapi.MemberIDAnonymous]; !ok {
		t.Error("Anonymous (member id -1) row not synthesized")
	}
}

// TestGetPermissionsTableFreeBusyMasking pins the IncludeFreeBusy table flag: without
// it, the free/busy bits are stripped from PR_MEMBER_RIGHTS before the row goes out,
// so the seeded 0xC00 Calendar default reads back as 0x400 (Visible only).
func TestGetPermissionsTableFreeBusyMasking(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	masked := permRows(t, dir, mapi.PrivateFIDCalendar, false)
	if r, _ := masked[mapi.MemberIDDefault].Get(mapi.PrMemberRights); r != int32(mapi.FrightsVisible) {
		t.Errorf("masked Default rights = %v, want 0x400 (free/busy bits stripped)", r)
	}
	full := permRows(t, dir, mapi.PrivateFIDCalendar, true)
	if r, _ := full[mapi.MemberIDDefault].Get(mapi.PrMemberRights); r != int32(0xC00) {
		t.Errorf("unmasked Default rights = %v, want 0xC00 (free/busy bits kept)", r)
	}
}

// TestGetPermissionsTableSynthesizesSpecials confirms that a folder with no stored
// permissions still serves the two always-present special members — Default (id 0)
// and Anonymous (id -1) — at rightsNone, so an Outlook dialog can edit them.
func TestGetPermissionsTableSynthesizesSpecials(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	rows := permRows(t, dir, mapi.PrivateFIDInbox, true)
	if len(rows) != 2 {
		t.Fatalf("clean folder served %d permission rows, want 2 (synthesized Default+Anonymous)", len(rows))
	}
	for _, id := range []int64{mapi.MemberIDDefault, mapi.MemberIDAnonymous} {
		r, ok := rows[id]
		if !ok {
			t.Errorf("synthesized member id %d missing", id)
			continue
		}
		if rights, _ := r.Get(mapi.PrMemberRights); rights != int32(mapi.RightsNone) {
			t.Errorf("synthesized member id %d rights = %v, want 0 (none)", id, rights)
		}
	}
}
