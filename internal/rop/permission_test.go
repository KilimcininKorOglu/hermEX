package rop

import (
	"testing"

	"hermex/internal/directory"
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

// openFolderSession opens a ROP session and an OpenFolder handle on the given folder,
// returning both so a test can drive permission ROPs against the handle.
func openFolderSession(t *testing.T, dir string, accounts directory.Accounts, folderFID uint64) (*Session, uint32) {
	t.Helper()
	sess := NewSession(dir, accounts, "owner@hermex.test")
	t.Cleanup(sess.Close)

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, folderFID))), []uint32{logonH, 0xFFFFFFFF})
	return sess, h[1]
}

// readPermRows reads a folder's permission rows on an existing session+folder handle
// (GetPermissionsTable -> SetColumns -> QueryRows), indexed by wire member id.
func readPermRows(t *testing.T, sess *Session, folderH uint32, includeFreeBusy bool) map[int64]mapi.PropertyValues {
	t.Helper()
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

// permRows opens a permission table on the given folder and reads its rows, returning
// them indexed by wire member id. It walks Logon -> OpenFolder -> GetPermissionsTable
// -> SetColumns -> QueryRows, the path an Outlook permissions dialog drives.
func permRows(t *testing.T, dir string, folderFID uint64, includeFreeBusy bool) map[int64]mapi.PropertyValues {
	t.Helper()
	sess, folderH := openFolderSession(t, dir, nil, folderFID)
	return readPermRows(t, sess, folderH, includeFreeBusy)
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

// permDataRow is one PermissionData a test sends in a RopModifyPermissions request.
type permDataRow struct {
	flags uint8
	props []mapi.TaggedPropVal
}

// buildModifyPermissions builds a RopModifyPermissions request over the folder at
// inIdx: ModifyFlags, PermissionsCount, then each PermissionData (flags + a
// TPROPVAL_ARRAY).
func buildModifyPermissions(t *testing.T, inIdx, modifyFlags uint8, rows []permDataRow) []byte {
	t.Helper()
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropModifyPermissions)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(modifyFlags)
	b.Uint16(uint16(len(rows)))
	for _, r := range rows {
		b.Uint8(r.flags)
		b.Uint16(uint16(len(r.props)))
		for _, tp := range r.props {
			if err := b.TaggedPropVal(tp); err != nil {
				t.Fatalf("encode TaggedPropVal %#x: %v", uint32(tp.Tag), err)
			}
		}
	}
	return b.Bytes()
}

// abMemberEntryID builds an address-book member EntryID in hermEX's GAL DN form
// (28-byte header + "/o=hermex/.../cn=<smtp>"), the identity Outlook sends in an
// AddRow for a GAL member.
func abMemberEntryID(smtp string) []byte {
	b := ext.NewPush(0)
	b.Uint32(0)             // flags
	b.Raw(make([]byte, 16)) // provider GUID (parser keys on the DN, not the GUID)
	b.Uint32(1)             // version
	b.Uint32(0)             // display type
	b.Raw([]byte("/o=hermex/ou=hermex/cn=Recipients/cn=" + smtp))
	b.Uint8(0) // NUL
	return b.Bytes()
}

// applyModify dispatches a RopModifyPermissions request and asserts the response is
// the bare MS-OXCPERM head (RopId + HandleIndex echoing the input + ReturnValue, no
// trailing bytes), returning nothing — the effect is read back via readPermRows.
func applyModify(t *testing.T, sess *Session, folderH uint32, modifyFlags uint8, rows []permDataRow) {
	t.Helper()
	resp, _ := sess.Dispatch(buildModifyPermissions(t, 0, modifyFlags, rows), []uint32{folderH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropModifyPermissions {
		t.Fatalf("RopId = %#x, want ModifyPermissions", id)
	}
	if h := mustU8(t, p, "hindex"); h != 0 {
		t.Errorf("ModifyPermissions HandleIndex = %d, want 0 (the input handle)", h)
	}
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("ModifyPermissions ReturnValue = %#x", ec)
	}
	if p.Remaining() != 0 {
		t.Fatalf("ModifyPermissions response has %d trailing bytes; the contract is a bare head", p.Remaining())
	}
}

// testAccounts is a directory with one resolvable member, so a real-member AddRow can
// be confirmed to exist.
func testAccounts(addr string) directory.Accounts {
	return directory.StaticAccounts{addr: {MailboxPath: "/tmp/" + addr}}
}

// TestModifyAddMemberViaEntryID drives the primary AddRow path: a real member
// identified by an address-book PR_ENTRYID whose DN embeds the SMTP address. The
// member must land in the table at a positive id with the granted rights — proving
// the EntryID parse and the directory existence check close end to end.
func TestModifyAddMemberViaEntryID(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	sess, folderH := openFolderSession(t, dir, testAccounts("bob@hermex.test"), mapi.PrivateFIDInbox)
	// IncludeFreeBusy: the client controls the free/busy bits, so the granted Author
	// rights are stored as sent rather than topped up with implied free/busy access.
	applyModify(t, sess, folderH, modifyPermIncludeFreeBusy, []permDataRow{{
		flags: permRowAdd,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrEntryID, Value: abMemberEntryID("bob@hermex.test")},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.RightsAuthor)},
		},
	}})

	rows := readPermRows(t, sess, folderH, true)
	var found bool
	for id, r := range rows {
		if name, _ := r.Get(mapi.PrMemberName); name == "bob@hermex.test" {
			found = true
			if id <= 0 {
				t.Errorf("added member id = %d, want a positive row id", id)
			}
			if rights, _ := r.Get(mapi.PrMemberRights); rights != int32(mapi.RightsAuthor) {
				t.Errorf("added member rights = %v, want 0x%X (Author)", rights, mapi.RightsAuthor)
			}
		}
	}
	if !found {
		t.Fatal("member added via PR_ENTRYID not found in the permission table")
	}
}

// TestModifyAddMemberViaSmtpAddress drives the fallback AddRow path: a member with no
// PR_ENTRYID, identified by a literal PR_SMTP_ADDRESS.
func TestModifyAddMemberViaSmtpAddress(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	sess, folderH := openFolderSession(t, dir, testAccounts("carol@hermex.test"), mapi.PrivateFIDInbox)
	applyModify(t, sess, folderH, 0, []permDataRow{{
		flags: permRowAdd,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrSmtpAddress, Value: "carol@hermex.test"},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.RightsReviewer)},
		},
	}})

	var found bool
	for _, r := range readPermRows(t, sess, folderH, true) {
		if name, _ := r.Get(mapi.PrMemberName); name == "carol@hermex.test" {
			found = true
		}
	}
	if !found {
		t.Error("member added via PR_SMTP_ADDRESS not found")
	}
}

// TestModifyUnknownMemberSkipped proves an AddRow for an address the directory does
// not know is skipped silently (no fault, no row), matching the reference.
func TestModifyUnknownMemberSkipped(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	sess, folderH := openFolderSession(t, dir, testAccounts("known@hermex.test"), mapi.PrivateFIDInbox)
	applyModify(t, sess, folderH, 0, []permDataRow{{
		flags: permRowAdd,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrSmtpAddress, Value: "stranger@hermex.test"},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.RightsReviewer)},
		},
	}})

	// Only the synthesized Default + Anonymous remain; the unknown member was skipped.
	if rows := readPermRows(t, sess, folderH, true); len(rows) != 2 {
		t.Errorf("unknown-member Add produced %d rows, want 2 (silently skipped)", len(rows))
	}
}

// TestModifyIngestMasksAndNormalizesRights pins the security-relevant ingest
// transform: a client's forbidden bits are masked off (store-owner 0x2000) and the
// implied rights are filled (ReadAny implies Visible), so what lands is the
// normalized, masked value — not the raw client bits.
func TestModifyIngestMasksAndNormalizesRights(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	const forbiddenStoreOwner = 0x2000
	sess, folderH := openFolderSession(t, dir, testAccounts("dave@hermex.test"), mapi.PrivateFIDInbox)
	applyModify(t, sess, folderH, modifyPermIncludeFreeBusy, []permDataRow{{
		flags: permRowAdd,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrSmtpAddress, Value: "dave@hermex.test"},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.FrightsReadAny | forbiddenStoreOwner)},
		},
	}})

	// IncludeFreeBusy was set, so no free/busy bits are auto-added; ReadAny implies
	// Visible; the store-owner bit is stripped.
	want := int32(mapi.FrightsReadAny | mapi.FrightsVisible)
	var got int32
	for _, r := range readPermRows(t, sess, folderH, true) {
		if name, _ := r.Get(mapi.PrMemberName); name == "dave@hermex.test" {
			v, _ := r.Get(mapi.PrMemberRights)
			got = v.(int32)
		}
	}
	if got != want {
		t.Errorf("ingested rights = 0x%X, want 0x%X (forbidden bit stripped, Visible implied)", got, want)
	}
}

// TestModifyDefaultThroughRop edits the synthesized Default member (wire id 0) on a
// folder with no stored default row and confirms the grant persists — the
// create-on-modify path, exercised end to end through the ROP layer.
func TestModifyDefaultThroughRop(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	sess, folderH := openFolderSession(t, dir, nil, mapi.PrivateFIDInbox)
	applyModify(t, sess, folderH, modifyPermIncludeFreeBusy, []permDataRow{{
		flags: permRowModify,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrMemberID, Value: mapi.MemberIDDefault},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.RightsReviewer)},
		},
	}})

	rows := readPermRows(t, sess, folderH, true)
	if r, _ := rows[mapi.MemberIDDefault].Get(mapi.PrMemberRights); r != int32(mapi.RightsReviewer) {
		t.Errorf("Default rights after edit = %v, want 0x%X (the grant was dropped)", r, mapi.RightsReviewer)
	}
}

// TestModifyFillsFreeBusyWhenNotIncluded pins the other normalization branch: when the
// client does NOT set IncludeFreeBusy, the server fills the implied free/busy bits, so
// a bare ReadAny grant gains Visible plus both free/busy levels.
func TestModifyFillsFreeBusyWhenNotIncluded(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	sess, folderH := openFolderSession(t, dir, testAccounts("grace@hermex.test"), mapi.PrivateFIDInbox)
	applyModify(t, sess, folderH, 0, []permDataRow{{
		flags: permRowAdd,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrSmtpAddress, Value: "grace@hermex.test"},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.FrightsReadAny)},
		},
	}})

	want := int32(mapi.FrightsReadAny | mapi.FrightsVisible | mapi.FrightsFreeBusySimple | mapi.FrightsFreeBusyDetailed)
	var got int32
	for _, r := range readPermRows(t, sess, folderH, true) {
		if name, _ := r.Get(mapi.PrMemberName); name == "grace@hermex.test" {
			v, _ := r.Get(mapi.PrMemberRights)
			got = v.(int32)
		}
	}
	if got != want {
		t.Errorf("rights without IncludeFreeBusy = 0x%X, want 0x%X (free/busy bits not filled)", got, want)
	}
}

// TestModifyRemoveMember adds a member then removes it by its wire member id, and
// confirms it is gone from the table.
func TestModifyRemoveMember(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	sess, folderH := openFolderSession(t, dir, testAccounts("erin@hermex.test"), mapi.PrivateFIDInbox)
	applyModify(t, sess, folderH, 0, []permDataRow{{
		flags: permRowAdd,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrSmtpAddress, Value: "erin@hermex.test"},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.RightsReviewer)},
		},
	}})

	var erinID int64
	for id, r := range readPermRows(t, sess, folderH, true) {
		if name, _ := r.Get(mapi.PrMemberName); name == "erin@hermex.test" {
			erinID = id
		}
	}
	if erinID <= 0 {
		t.Fatal("added member did not get a positive id")
	}

	applyModify(t, sess, folderH, 0, []permDataRow{{
		flags: permRowRemove,
		props: []mapi.TaggedPropVal{{Tag: mapi.PrMemberID, Value: erinID}},
	}})

	if _, ok := readPermRows(t, sess, folderH, true)[erinID]; ok {
		t.Errorf("member id %d still present after Remove", erinID)
	}
}

// TestModifyReplaceRows confirms the ReplaceRows flag wipes the folder's stored
// permissions before applying the batch: the seeded Calendar default is cleared, and
// only the new member (plus the always-synthesized specials) remains.
func TestModifyReplaceRows(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	sess, folderH := openFolderSession(t, dir, testAccounts("frank@hermex.test"), mapi.PrivateFIDCalendar)
	// Precondition: the Calendar carries a stored default free/busy grant (0xC00).
	if r, _ := readPermRows(t, sess, folderH, true)[mapi.MemberIDDefault].Get(mapi.PrMemberRights); r != int32(0xC00) {
		t.Fatalf("precondition: seeded Calendar default rights = %v, want 0xC00", r)
	}

	applyModify(t, sess, folderH, modifyPermReplaceRows, []permDataRow{{
		flags: permRowAdd,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrSmtpAddress, Value: "frank@hermex.test"},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.RightsEditor)},
		},
	}})

	rows := readPermRows(t, sess, folderH, true)
	// The stored default was wiped, so its row is now synthesized at rightsNone.
	if r, _ := rows[mapi.MemberIDDefault].Get(mapi.PrMemberRights); r != int32(mapi.RightsNone) {
		t.Errorf("after ReplaceRows the Default rights = %v, want 0 (stored grant not cleared)", r)
	}
	var foundFrank bool
	for _, r := range rows {
		if name, _ := r.Get(mapi.PrMemberName); name == "frank@hermex.test" {
			foundFrank = true
		}
	}
	if !foundFrank {
		t.Error("the replacement member is missing after ReplaceRows")
	}
}
