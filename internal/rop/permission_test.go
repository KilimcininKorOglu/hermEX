package rop

import (
	"fmt"
	"maps"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/ext"
	"hermex/internal/mapi"
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

	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, folderFID))), ropOpenFolder, "OpenFolder", h[0], 0xFFFFFFFF)
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
	ropOK(t, dispatchOn(sess, buildSetColumns(0, cols), tableH), ropSetColumns, "SetColumns(permissions)")
	qr := dispatchOn(sess, buildQueryRows(0, 0, 1, 64), tableH)

	seekPos, rows := queryRowsResponse(t, qr, cols)
	// A read that returns every row leaves the cursor at the end ([MS-OXCTABL]
	// 2.2.2.5 Origin), as TestQueryRows pins for the contents table.
	if seekPos != bookmarkEnd {
		t.Errorf("permissions QueryRows Origin = %#x, want END (%#x) after the last row", seekPos, bookmarkEnd)
	}
	// Every folder carries its default and anonymous rows ([MS-OXCPERM] 2.2.4), so
	// an empty table is a failed read, not a folder without members.
	if len(rows) == 0 {
		t.Fatal("the permissions table returned no rows")
	}
	out := make(map[int64]mapi.PropertyValues, len(rows))
	for _, r := range rows {
		v, ok := r.Get(mapi.PrMemberID)
		if !ok {
			t.Fatalf("permission row without PR_MEMBER_ID: %v", r)
		}
		id, ok := v.(int64)
		if !ok {
			t.Fatalf("PR_MEMBER_ID = %T, want int64 (PtI8)", v)
		}
		out[id] = r
	}
	return out
}

// memberRights reads a row's PR_MEMBER_RIGHTS, failing when the row lacks it.
func memberRights(t *testing.T, row mapi.PropertyValues) int32 {
	t.Helper()
	v, ok := row.Get(mapi.PrMemberRights)
	if !ok {
		t.Fatalf("permission row without PR_MEMBER_RIGHTS: %v", row)
	}
	r, ok := v.(int32)
	if !ok {
		t.Fatalf("PR_MEMBER_RIGHTS = %T, want int32 (PtLong)", v)
	}
	return r
}

// memberName reads a row's PR_MEMBER_NAME, failing when the row lacks it.
func memberName(t *testing.T, row mapi.PropertyValues) string {
	t.Helper()
	v, ok := row.Get(mapi.PrMemberName)
	if !ok {
		t.Fatalf("permission row without PR_MEMBER_NAME: %v", row)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("PR_MEMBER_NAME = %T, want string", v)
	}
	return s
}

// findMember returns the row and wire id of the member with the given name.
func findMember(t *testing.T, rows map[int64]mapi.PropertyValues, name string) (int64, mapi.PropertyValues, bool) {
	t.Helper()
	for id, r := range rows {
		if memberName(t, r) == name {
			return id, r, true
		}
	}
	return 0, nil, false
}

// permState renders a permission table as id -> "name/rights", so two reads can be
// compared whole: a refused or skipped change must leave every row as it was.
func permState(t *testing.T, rows map[int64]mapi.PropertyValues) map[int64]string {
	t.Helper()
	out := make(map[int64]string, len(rows))
	for id, r := range rows {
		out[id] = fmt.Sprintf("%s/%#x", memberName(t, r), memberRights(t, r))
	}
	return out
}

// wantPermState fails when the folder's table differs from before.
func wantPermState(t *testing.T, sess *Session, folderH uint32, before map[int64]string, what string) {
	t.Helper()
	if after := permState(t, readPermRows(t, sess, folderH, true)); !maps.Equal(before, after) {
		t.Errorf("%s changed the permission table:\nbefore %v\nafter  %v", what, before, after)
	}
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
	rows := permRows(t, provisionedDir(t), mapi.PrivateFIDCalendar, true)
	def, ok := rows[mapi.MemberIDDefault]
	if !ok {
		t.Fatal("no Default (member id 0) row in the seeded Calendar permissions")
	}
	if name := memberName(t, def); name != "default" {
		t.Errorf("Default member name = %q, want \"default\"", name)
	}
	// With IncludeFreeBusy the seeded FreeBusySimple|Visible (0xC00) is kept whole.
	if r := memberRights(t, def); r != int32(0xC00) {
		t.Errorf("Default rights = %#x, want 0xC00 (FreeBusySimple|Visible)", r)
	}
	if anon, ok := rows[mapi.MemberIDAnonymous]; !ok {
		t.Error("Anonymous (member id -1) row not synthesized")
	} else if r := memberRights(t, anon); r != int32(mapi.RightsNone) {
		t.Errorf("Anonymous rights = %#x, want 0", r)
	}
}

// TestGetPermissionsTableFreeBusyMasking pins the IncludeFreeBusy table flag: without
// it, the free/busy bits are stripped from PR_MEMBER_RIGHTS before the row goes out,
// so the seeded 0xC00 Calendar default reads back as 0x400 (Visible only).
func TestGetPermissionsTableFreeBusyMasking(t *testing.T) {
	dir := provisionedDir(t)
	masked := permRows(t, dir, mapi.PrivateFIDCalendar, false)
	if r := memberRights(t, masked[mapi.MemberIDDefault]); r != int32(mapi.FrightsVisible) {
		t.Errorf("masked Default rights = %#x, want 0x400 (free/busy bits stripped)", r)
	}
	full := permRows(t, dir, mapi.PrivateFIDCalendar, true)
	if r := memberRights(t, full[mapi.MemberIDDefault]); r != int32(0xC00) {
		t.Errorf("unmasked Default rights = %#x, want 0xC00 (free/busy bits kept)", r)
	}
}

// TestGetPermissionsTableSynthesizesSpecials confirms that a folder with no stored
// permissions still serves the two always-present special members, Default (id 0)
// and Anonymous (id -1), at rightsNone, so an Outlook dialog can edit them.
func TestGetPermissionsTableSynthesizesSpecials(t *testing.T) {
	rows := permRows(t, provisionedDir(t), mapi.PrivateFIDInbox, true)
	if len(rows) != 2 {
		t.Fatalf("clean folder served %d permission rows, want 2 (synthesized Default+Anonymous)", len(rows))
	}
	for _, id := range []int64{mapi.MemberIDDefault, mapi.MemberIDAnonymous} {
		r, ok := rows[id]
		if !ok {
			t.Errorf("synthesized member id %d missing", id)
			continue
		}
		if rights := memberRights(t, r); rights != int32(mapi.RightsNone) {
			t.Errorf("synthesized member id %d rights = %#x, want 0 (none)", id, rights)
		}
	}
}

// permDataRow is one PermissionData a test sends in a RopModifyPermissions request.
type permDataRow struct {
	flags uint8
	props []mapi.TaggedPropVal
}

// buildModifyPermissionsCount builds a RopModifyPermissions request over the folder
// at inIdx whose PermissionsCount field is count, followed by the given rows. A count
// above len(rows) is the truncated request a hostile client sends.
func buildModifyPermissionsCount(t *testing.T, inIdx, modifyFlags uint8, count uint16, rows []permDataRow) []byte {
	t.Helper()
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropModifyPermissions)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(modifyFlags)
	b.Uint16(count)
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

// buildModifyPermissions builds a RopModifyPermissions request over the folder at
// inIdx: ModifyFlags, PermissionsCount, then each PermissionData (flags + a
// TPROPVAL_ARRAY).
func buildModifyPermissions(t *testing.T, inIdx, modifyFlags uint8, rows []permDataRow) []byte {
	t.Helper()
	return buildModifyPermissionsCount(t, inIdx, modifyFlags, uint16(len(rows)), rows)
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
// trailing bytes), returning nothing, the effect is read back via readPermRows.
func applyModify(t *testing.T, sess *Session, folderH uint32, modifyFlags uint8, rows []permDataRow) {
	t.Helper()
	resp := dispatchOn(sess, buildModifyPermissions(t, 0, modifyFlags, rows), folderH)
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

// testAccounts is a directory with one resolvable member beside the session owner,
// so a real-member AddRow can be confirmed to exist; a member is granted only when
// it shares the owner's organization (static accounts scope by domain).
func testAccounts(addr string) directory.Accounts {
	return directory.StaticAccounts{
		addr:                {MailboxPath: "/tmp/" + addr},
		"owner@hermex.test": {MailboxPath: "/tmp/owner@hermex.test"},
	}
}

// addRow is an AddRow PermissionData naming a member by SMTP address.
func addRow(smtp string, rights uint32) permDataRow {
	return permDataRow{flags: permRowAdd, props: []mapi.TaggedPropVal{
		{Tag: mapi.PrSmtpAddress, Value: smtp},
		{Tag: mapi.PrMemberRights, Value: int32(rights)},
	}}
}

// TestModifyMemberOfAnotherOrganizationSkipped: a grant opens the mailbox to its
// member, which the logon keeps inside the organization, so an AddRow naming an
// account of another organization (by EntryID or by SMTP address) is skipped
// like an unknown member and stores no row.
func TestModifyMemberOfAnotherOrganizationSkipped(t *testing.T) {
	sess, folderH := openFolderSession(t, provisionedDir(t), testAccounts("mallory@other-org.test"), mapi.PrivateFIDInbox)
	before := permState(t, readPermRows(t, sess, folderH, true))
	applyModify(t, sess, folderH, 0, []permDataRow{
		{flags: permRowAdd, props: []mapi.TaggedPropVal{
			{Tag: mapi.PrEntryID, Value: abMemberEntryID("mallory@other-org.test")},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.RightsReviewer)},
		}},
		addRow("mallory@other-org.test", mapi.RightsReviewer),
	})
	wantPermState(t, sess, folderH, before, "an AddRow for another organization's member")
}

// TestReviewerCannotRaiseTheirOwnRights proves a delegate holding Reviewer on the
// Inbox cannot rewrite its permission table: the ADD that names the delegate as
// Owner answers ecAccessDenied and the delegate's rights stay Reviewer. The
// session has no directory, which accepts the member address as given, so a
// missing gate would store the row.
func TestReviewerCannotRaiseTheirOwnRights(t *testing.T) {
	dir := provisionedDir(t)
	const delegate = "delegate@hermex.test"
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDInbox), delegate, mapi.RightsReviewer)

	sess, logonH := delegateLogon(t, dir, delegate)
	defer sess.Close()
	h := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), ropOpenFolder, "OpenFolder", logonH, 0xFFFFFFFF)
	resp := dispatchOn(sess, buildModifyPermissions(t, 0, modifyPermIncludeFreeBusy, []permDataRow{
		addRow(delegate, mapi.RightsOwner),
	}), h[1])
	if ec := ropResultEC(t, resp); ec != ecAccessDenied {
		t.Errorf("Reviewer ModifyPermissions(ADD self as Owner) ec = %#x, want AccessDenied", ec)
	}
	rights, err := sess.get(logonH).store.ResolvePermission(int64(mapi.PrivateFIDInbox), delegate)
	if err != nil {
		t.Fatal(err)
	}
	if rights != mapi.RightsReviewer {
		t.Errorf("delegate rights after the refused change = %#x, want Reviewer %#x", rights, mapi.RightsReviewer)
	}
}

// TestModifyAddMemberViaEntryID drives the primary AddRow path: a real member
// identified by an address-book PR_ENTRYID whose DN embeds the SMTP address. The
// member must land in the table at a positive id with the granted rights, proving
// the EntryID parse and the directory existence check close end to end.
func TestModifyAddMemberViaEntryID(t *testing.T) {
	sess, folderH := openFolderSession(t, provisionedDir(t), testAccounts("bob@hermex.test"), mapi.PrivateFIDInbox)
	// IncludeFreeBusy: the client controls the free/busy bits, so the granted Author
	// rights are stored as sent rather than topped up with implied free/busy access.
	applyModify(t, sess, folderH, modifyPermIncludeFreeBusy, []permDataRow{{
		flags: permRowAdd,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrEntryID, Value: abMemberEntryID("bob@hermex.test")},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.RightsAuthor)},
		},
	}})

	id, row, found := findMember(t, readPermRows(t, sess, folderH, true), "bob@hermex.test")
	if !found {
		t.Fatal("member added via PR_ENTRYID not found in the permission table")
	}
	if id <= 0 {
		t.Errorf("added member id = %d, want a positive row id", id)
	}
	if rights := memberRights(t, row); rights != int32(mapi.RightsAuthor) {
		t.Errorf("added member rights = %#x, want %#x (Author)", rights, mapi.RightsAuthor)
	}
}

// TestModifyAddMemberViaSmtpAddress drives the fallback AddRow path: a member with no
// PR_ENTRYID, identified by a literal PR_SMTP_ADDRESS.
func TestModifyAddMemberViaSmtpAddress(t *testing.T) {
	sess, folderH := openFolderSession(t, provisionedDir(t), testAccounts("carol@hermex.test"), mapi.PrivateFIDInbox)
	applyModify(t, sess, folderH, 0, []permDataRow{addRow("carol@hermex.test", mapi.RightsReviewer)})
	id, row, found := findMember(t, readPermRows(t, sess, folderH, true), "carol@hermex.test")
	if !found || id <= 0 {
		t.Fatalf("member added via PR_SMTP_ADDRESS: found=%v id=%d, want a positive id", found, id)
	}
	if r := memberRights(t, row); r != int32(mapi.RightsReviewer|mapi.FrightsFreeBusySimple|mapi.FrightsFreeBusyDetailed) {
		t.Errorf("added member rights = %#x, want Reviewer + free/busy", r)
	}
}

// TestModifyUnknownMemberSkipped proves an AddRow for an address the directory does
// not know is skipped silently (no fault, no row), matching the reference.
func TestModifyUnknownMemberSkipped(t *testing.T) {
	sess, folderH := openFolderSession(t, provisionedDir(t), testAccounts("known@hermex.test"), mapi.PrivateFIDInbox)
	before := permState(t, readPermRows(t, sess, folderH, true))
	applyModify(t, sess, folderH, 0, []permDataRow{addRow("stranger@hermex.test", mapi.RightsReviewer)})
	wantPermState(t, sess, folderH, before, "an AddRow for an unknown member")
}

// TestModifyShortEntryIDSkipped sends an AddRow whose PR_ENTRYID is shorter than the
// fixed address-book EntryID header ([MS-OXCDATA] 2.2.5.2: 28 bytes before the DN)
// and carries no PR_SMTP_ADDRESS. The row names no member, so it is skipped and the
// table is unchanged; reading a DN past the end of such an id would fault the batch.
func TestModifyShortEntryIDSkipped(t *testing.T) {
	sess, folderH := openFolderSession(t, provisionedDir(t), nil, mapi.PrivateFIDInbox)
	before := permState(t, readPermRows(t, sess, folderH, true))
	applyModify(t, sess, folderH, 0, []permDataRow{{
		flags: permRowAdd,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrEntryID, Value: []byte("/cn=x@hermex.test")},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.RightsOwner)},
		},
	}})
	wantPermState(t, sess, folderH, before, "an AddRow with a truncated EntryID")
}

// TestModifyUnknownMemberIDLeavesTable sends a Modify and a Remove naming a member id
// the folder does not hold. Neither may touch another member's row: the stored
// grant keeps its rights and the table reads back unchanged.
func TestModifyUnknownMemberIDLeavesTable(t *testing.T) {
	sess, folderH := openFolderSession(t, provisionedDir(t), testAccounts("ivan@hermex.test"), mapi.PrivateFIDInbox)
	applyModify(t, sess, folderH, 0, []permDataRow{addRow("ivan@hermex.test", mapi.RightsReviewer)})
	before := permState(t, readPermRows(t, sess, folderH, true))

	const unknownID = int64(1 << 40)
	applyModify(t, sess, folderH, modifyPermIncludeFreeBusy, []permDataRow{
		{flags: permRowModify, props: []mapi.TaggedPropVal{
			{Tag: mapi.PrMemberID, Value: unknownID},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.RightsOwner)},
		}},
		{flags: permRowRemove, props: []mapi.TaggedPropVal{{Tag: mapi.PrMemberID, Value: unknownID}}},
	})
	wantPermState(t, sess, folderH, before, "a Modify and Remove of an unknown member id")
}

// TestModifyPermissionsTruncatedRequestAppliesNothing sends a RopModifyPermissions
// whose PermissionsCount (2) promises more PermissionData than the request carries
// (1). The request cannot be framed, so the batch ends with no response for it
// (an unframed ROP cannot be skipped) and the one complete row is not applied.
func TestModifyPermissionsTruncatedRequestAppliesNothing(t *testing.T) {
	sess, folderH := openFolderSession(t, provisionedDir(t), testAccounts("judy@hermex.test"), mapi.PrivateFIDInbox)
	before := permState(t, readPermRows(t, sess, folderH, true))

	req := buildModifyPermissionsCount(t, 0, 0, 2, []permDataRow{addRow("judy@hermex.test", mapi.RightsEditor)})
	if resp := dispatchOn(sess, req, folderH); len(resp) != 0 {
		t.Errorf("truncated ModifyPermissions answered %x, want no response", resp)
	}
	wantPermState(t, sess, folderH, before, "a truncated ModifyPermissions")
}

// TestModifyIngestMasksAndNormalizesRights pins the security-relevant ingest
// transform: a client's forbidden bits are masked off (store-owner 0x2000) and the
// implied rights are filled (ReadAny implies Visible), so what lands is the
// normalized, masked value, not the raw client bits.
func TestModifyIngestMasksAndNormalizesRights(t *testing.T) {
	const forbiddenStoreOwner = 0x2000
	sess, folderH := openFolderSession(t, provisionedDir(t), testAccounts("dave@hermex.test"), mapi.PrivateFIDInbox)
	applyModify(t, sess, folderH, modifyPermIncludeFreeBusy, []permDataRow{
		addRow("dave@hermex.test", mapi.FrightsReadAny|forbiddenStoreOwner),
	})

	// IncludeFreeBusy was set, so no free/busy bits are auto-added; ReadAny implies
	// Visible; the store-owner bit is stripped.
	want := int32(mapi.FrightsReadAny | mapi.FrightsVisible)
	id, row, found := findMember(t, readPermRows(t, sess, folderH, true), "dave@hermex.test")
	if !found || id <= 0 {
		t.Fatalf("member dave@hermex.test: found=%v id=%d, want a positive id", found, id)
	}
	if got := memberRights(t, row); got != want {
		t.Errorf("ingested rights = %#x, want %#x (forbidden bit stripped, Visible implied)", got, want)
	}
}

// TestModifyDefaultThroughRop edits the synthesized Default member (wire id 0) on a
// folder with no stored default row and confirms the grant persists, the
// create-on-modify path, exercised end to end through the ROP layer.
func TestModifyDefaultThroughRop(t *testing.T) {
	sess, folderH := openFolderSession(t, provisionedDir(t), nil, mapi.PrivateFIDInbox)
	applyModify(t, sess, folderH, modifyPermIncludeFreeBusy, []permDataRow{{
		flags: permRowModify,
		props: []mapi.TaggedPropVal{
			{Tag: mapi.PrMemberID, Value: mapi.MemberIDDefault},
			{Tag: mapi.PrMemberRights, Value: int32(mapi.RightsReviewer)},
		},
	}})

	rows := readPermRows(t, sess, folderH, true)
	if r := memberRights(t, rows[mapi.MemberIDDefault]); r != int32(mapi.RightsReviewer) {
		t.Errorf("Default rights after edit = %#x, want %#x (the grant was dropped)", r, mapi.RightsReviewer)
	}
}

// TestModifyFillsFreeBusyWhenNotIncluded pins the other normalization branch: when the
// client does NOT set IncludeFreeBusy, the server fills the implied free/busy bits, so
// a bare ReadAny grant gains Visible plus both free/busy levels.
func TestModifyFillsFreeBusyWhenNotIncluded(t *testing.T) {
	sess, folderH := openFolderSession(t, provisionedDir(t), testAccounts("grace@hermex.test"), mapi.PrivateFIDInbox)
	applyModify(t, sess, folderH, 0, []permDataRow{addRow("grace@hermex.test", mapi.FrightsReadAny)})

	want := int32(mapi.FrightsReadAny | mapi.FrightsVisible | mapi.FrightsFreeBusySimple | mapi.FrightsFreeBusyDetailed)
	id, row, found := findMember(t, readPermRows(t, sess, folderH, true), "grace@hermex.test")
	if !found || id <= 0 {
		t.Fatalf("member grace@hermex.test: found=%v id=%d, want a positive id", found, id)
	}
	if got := memberRights(t, row); got != want {
		t.Errorf("rights without IncludeFreeBusy = %#x, want %#x (free/busy bits not filled)", got, want)
	}
}

// TestModifyRemoveMember adds a member then removes it by its wire member id, and
// confirms it is gone from the table.
func TestModifyRemoveMember(t *testing.T) {
	sess, folderH := openFolderSession(t, provisionedDir(t), testAccounts("erin@hermex.test"), mapi.PrivateFIDInbox)
	applyModify(t, sess, folderH, 0, []permDataRow{addRow("erin@hermex.test", mapi.RightsReviewer)})

	erinID, erinRow, found := findMember(t, readPermRows(t, sess, folderH, true), "erin@hermex.test")
	if !found || erinID <= 0 {
		t.Fatalf("added member: found=%v id=%d, want a positive id", found, erinID)
	}
	if r := memberRights(t, erinRow); r&int32(mapi.RightsReviewer) != int32(mapi.RightsReviewer) {
		t.Errorf("added member rights = %#x, want Reviewer bits set", r)
	}

	applyModify(t, sess, folderH, 0, []permDataRow{{
		flags: permRowRemove,
		props: []mapi.TaggedPropVal{{Tag: mapi.PrMemberID, Value: erinID}},
	}})

	if rem, ok := readPermRows(t, sess, folderH, true)[erinID]; ok {
		t.Errorf("member id %d still present after Remove: %v", erinID, rem)
	}
}

// TestModifyPermissionsBatchAlignment proves the RopModifyPermissions request decoder
// consumes its body exactly, ModifyFlags, the count, and every PermissionData with
// its TPROPVAL_ARRAY, so a following ROP in the same Execute batch parses from the
// right offset. A short or long read would surface as the second RopId coming back
// wrong. The single-ROP tests pin the response framing; this pins the request side.
func TestModifyPermissionsBatchAlignment(t *testing.T) {
	sess, folderH := openFolderSession(t, provisionedDir(t), testAccounts("heidi@hermex.test"), mapi.PrivateFIDInbox)

	// Batch: ModifyPermissions (a non-trivial PermissionData) then GetPermissionsTable.
	modify := buildModifyPermissions(t, 0, modifyPermIncludeFreeBusy, []permDataRow{
		addRow("heidi@hermex.test", mapi.RightsEditor),
	})
	batch := append(modify, buildGetPermissionsTable(0, 1, permTableIncludeFreeBusy)...)

	p := ext.NewPull(dispatchOn(sess, batch, folderH, 0xFFFFFFFF), ext.FlagUTF16)

	if id := mustU8(t, p, "RopId"); id != ropModifyPermissions {
		t.Fatalf("first RopId = %#x, want ModifyPermissions", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("ModifyPermissions ec = %#x", ec)
	}
	// The second response can only land here if the decoder stayed aligned.
	if id := mustU8(t, p, "RopId"); id != ropGetPermissionsTable {
		t.Fatalf("second RopId = %#x, want GetPermissionsTable (request decoder misaligned)", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Errorf("GetPermissionsTable ec = %#x", ec)
	}
}

// TestModifyReplaceRows confirms the ReplaceRows flag wipes the folder's stored
// permissions before applying the batch: the seeded Calendar default is cleared, and
// only the new member (plus the always-synthesized specials) remains.
func TestModifyReplaceRows(t *testing.T) {
	sess, folderH := openFolderSession(t, provisionedDir(t), testAccounts("frank@hermex.test"), mapi.PrivateFIDCalendar)
	// Precondition: the Calendar carries a stored default free/busy grant (0xC00).
	if r := memberRights(t, readPermRows(t, sess, folderH, true)[mapi.MemberIDDefault]); r != int32(0xC00) {
		t.Fatalf("precondition: seeded Calendar default rights = %#x, want 0xC00", r)
	}

	applyModify(t, sess, folderH, modifyPermReplaceRows, []permDataRow{addRow("frank@hermex.test", mapi.RightsEditor)})

	rows := readPermRows(t, sess, folderH, true)
	// The stored default was wiped, so its row is now synthesized at rightsNone.
	if r := memberRights(t, rows[mapi.MemberIDDefault]); r != int32(mapi.RightsNone) {
		t.Errorf("after ReplaceRows the Default rights = %#x, want 0 (stored grant not cleared)", r)
	}
	id, row, found := findMember(t, rows, "frank@hermex.test")
	if !found || id <= 0 {
		t.Fatalf("replacement member: found=%v id=%d, want a positive id", found, id)
	}
	if r := memberRights(t, row); r&int32(mapi.RightsEditor) != int32(mapi.RightsEditor) {
		t.Errorf("replacement member rights = %#x, want Editor bits set", r)
	}
}
