package objectstore

import (
	"testing"

	"hermex/internal/mapi"
)

// permByMember indexes a folder's permission rows by their wire member id so a test
// can assert one member's row without depending on row order.
func permByMember(t *testing.T, s *Store, folderID int64) map[int64]PermissionEntry {
	t.Helper()
	entries, err := s.ListPermissions(folderID)
	if err != nil {
		t.Fatalf("ListPermissions(%#x): %v", folderID, err)
	}
	m := make(map[int64]PermissionEntry, len(entries))
	for _, e := range entries {
		if _, dup := m[e.MemberID]; dup {
			t.Fatalf("duplicate member id %d in folder %#x", e.MemberID, folderID)
		}
		m[e.MemberID] = e
	}
	return m
}

// TestListPermissionsTranslatesMemberIDs is the central MS-OXCPERM invariant: the
// stored "default"/"" usernames surface as the wire member ids 0/-1 (never their
// row ids), real members surface as their own row id and username, and the anonymous
// row presents the display name "anonymous". A regression here passes a naive
// round-trip but breaks Outlook, which always addresses Default by id 0.
func TestListPermissionsTranslatesMemberIDs(t *testing.T) {
	s := openSeededStore(t)
	fid := int64(mapi.PrivateFIDInbox) // a folder with no seeded permissions

	// Insert one of each member kind directly, isolating the read translation.
	for _, r := range []struct {
		username string
		perm     int
	}{
		{"default", int(mapi.RightsReviewer)},
		{"", int(mapi.FrightsFreeBusySimple)}, // anonymous
		{"alice@test", int(mapi.RightsEditor)},
	} {
		if _, err := s.objdb.Exec(
			`INSERT INTO permissions (folder_id, username, permission) VALUES (?, ?, ?)`,
			fid, r.username, r.perm); err != nil {
			t.Fatal(err)
		}
	}

	m := permByMember(t, s, fid)
	if len(m) != 3 {
		t.Fatalf("got %d rows, want 3", len(m))
	}
	if e := m[mapi.MemberIDDefault]; e.Name != "default" || e.Rights != mapi.RightsReviewer {
		t.Errorf("default member = %+v, want id 0/name default/rights 0x%X", e, mapi.RightsReviewer)
	}
	if e := m[mapi.MemberIDAnonymous]; e.Name != "anonymous" || e.Rights != mapi.FrightsFreeBusySimple {
		t.Errorf("anonymous member = %+v, want id -1/name anonymous/rights 0x%X", e, mapi.FrightsFreeBusySimple)
	}
	// The real member's id is its row id — some value that is neither 0 nor -1.
	var real PermissionEntry
	for id, e := range m {
		if id != mapi.MemberIDDefault && id != mapi.MemberIDAnonymous {
			real = e
		}
	}
	if real.MemberID <= 0 || real.Name != "alice@test" || real.Rights != mapi.RightsEditor {
		t.Errorf("real member = %+v, want a positive row id/name alice@test/rights 0x%X", real, mapi.RightsEditor)
	}
}

// TestModifyDefaultAddressesSeededRow guards the bug the central invariant exists to
// prevent: a client editing the Default member (always wire id 0) must update the
// seeded "default" row in place, not spawn a new row keyed by a row id. The seeded
// Calendar folder already carries a default free/busy row.
func TestModifyDefaultAddressesSeededRow(t *testing.T) {
	s := openSeededStore(t)
	fid := int64(mapi.PrivateFIDCalendar)

	before := permByMember(t, s, fid)
	if e, ok := before[mapi.MemberIDDefault]; !ok || e.Rights != 0xC00 {
		t.Fatalf("seeded default = %+v (ok=%v), want id 0/rights 0xC00", e, ok)
	}

	// Edit the default member by its wire id 0.
	if err := s.ModifyPermissions(fid, false, []PermissionChange{
		{Op: PermModify, MemberID: mapi.MemberIDDefault, Rights: mapi.RightsReviewer},
	}); err != nil {
		t.Fatal(err)
	}

	after := permByMember(t, s, fid)
	if len(after) != 1 {
		t.Fatalf("after editing Default the folder has %d rows, want 1 (a new row was orphaned)", len(after))
	}
	if e := after[mapi.MemberIDDefault]; e.Rights != mapi.RightsReviewer {
		t.Errorf("default rights = 0x%X, want 0x%X (the seeded row was not updated)", e.Rights, mapi.RightsReviewer)
	}
}

// TestAddRealMemberThenModifyAndRemove walks a real member's lifecycle: an Add by
// resolved username creates a row with a positive id, a Modify by that id changes its
// rights, and a Remove by that id drops it.
func TestAddRealMemberThenModifyAndRemove(t *testing.T) {
	s := openSeededStore(t)
	fid := int64(mapi.PrivateFIDInbox)

	if err := s.ModifyPermissions(fid, false, []PermissionChange{
		{Op: PermAdd, Username: "bob@test", Rights: mapi.RightsReviewer},
	}); err != nil {
		t.Fatal(err)
	}
	m := permByMember(t, s, fid)
	if len(m) != 1 {
		t.Fatalf("after Add got %d rows, want 1", len(m))
	}
	var bobID int64
	for id, e := range m {
		if e.Name != "bob@test" {
			t.Fatalf("added member name = %q, want bob@test", e.Name)
		}
		if id <= 0 {
			t.Fatalf("real member id = %d, want a positive row id", id)
		}
		bobID = id
	}

	// Modify by the real row id.
	if err := s.ModifyPermissions(fid, false, []PermissionChange{
		{Op: PermModify, MemberID: bobID, Rights: mapi.RightsEditor},
	}); err != nil {
		t.Fatal(err)
	}
	if e := permByMember(t, s, fid)[bobID]; e.Rights != mapi.RightsEditor {
		t.Errorf("after Modify rights = 0x%X, want 0x%X", e.Rights, mapi.RightsEditor)
	}

	// Remove by the real row id.
	if err := s.ModifyPermissions(fid, false, []PermissionChange{
		{Op: PermRemove, MemberID: bobID},
	}); err != nil {
		t.Fatal(err)
	}
	if m := permByMember(t, s, fid); len(m) != 0 {
		t.Errorf("after Remove got %d rows, want 0", len(m))
	}
}

// TestModifyUnstoredDefaultCreatesRow guards the synthesized-member edit: a folder
// with no stored Default row still presents one (id 0) to the client, so a Modify of
// it must CREATE the row rather than no-op an UPDATE that silently drops the grant.
// The Inbox carries no seeded permissions.
func TestModifyUnstoredDefaultCreatesRow(t *testing.T) {
	s := openSeededStore(t)
	fid := int64(mapi.PrivateFIDInbox)

	if m := permByMember(t, s, fid); len(m) != 0 {
		t.Fatalf("precondition: Inbox has %d stored permission rows, want 0", len(m))
	}
	if err := s.ModifyPermissions(fid, false, []PermissionChange{
		{Op: PermModify, MemberID: mapi.MemberIDDefault, Rights: mapi.RightsReviewer},
	}); err != nil {
		t.Fatal(err)
	}
	m := permByMember(t, s, fid)
	if e, ok := m[mapi.MemberIDDefault]; !ok || e.Rights != mapi.RightsReviewer {
		t.Errorf("after editing the synthesized Default: row = %+v (ok=%v), want id 0/rights 0x%X (the edit was dropped)", e, ok, mapi.RightsReviewer)
	}
}

// TestAddAnonymousStoresEmptyUsername proves an anonymous Add (wire id -1) lands as
// the empty-username row and reads back as id -1 / "anonymous".
func TestAddAnonymousStoresEmptyUsername(t *testing.T) {
	s := openSeededStore(t)
	fid := int64(mapi.PrivateFIDInbox)

	if err := s.ModifyPermissions(fid, false, []PermissionChange{
		{Op: PermAdd, MemberID: mapi.MemberIDAnonymous, Rights: mapi.FrightsFreeBusySimple},
	}); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.objdb.QueryRow(
		`SELECT username FROM permissions WHERE folder_id=? AND permission=?`,
		fid, int(mapi.FrightsFreeBusySimple)).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "" {
		t.Errorf("anonymous stored username = %q, want empty string", stored)
	}
	if e := permByMember(t, s, fid)[mapi.MemberIDAnonymous]; e.Name != "anonymous" {
		t.Errorf("anonymous read back = %+v, want id -1/name anonymous", e)
	}
}

// TestModifyPermissionsReplaceClears proves the REPLACEROWS flag wipes the folder's
// existing rows before applying the batch: the seeded Calendar default is gone, only
// the new member remains.
func TestModifyPermissionsReplaceClears(t *testing.T) {
	s := openSeededStore(t)
	fid := int64(mapi.PrivateFIDCalendar)

	if _, ok := permByMember(t, s, fid)[mapi.MemberIDDefault]; !ok {
		t.Fatal("precondition: seeded Calendar default missing")
	}
	if err := s.ModifyPermissions(fid, true, []PermissionChange{
		{Op: PermAdd, Username: "carol@test", Rights: mapi.RightsAuthor},
	}); err != nil {
		t.Fatal(err)
	}
	m := permByMember(t, s, fid)
	if len(m) != 1 {
		t.Fatalf("after REPLACEROWS got %d rows, want 1 (the seeded default was not cleared)", len(m))
	}
	for _, e := range m {
		if e.Name != "carol@test" || e.Rights != mapi.RightsAuthor {
			t.Errorf("surviving row = %+v, want carol@test/Author", e)
		}
	}
}

// TestAddDefaultUpsertsNoDuplicate proves an Add addressing the Default member (wire
// id 0) upserts the single "default" row rather than inserting a duplicate, so the
// unique (folder_id, username) index is respected through the public API.
func TestAddDefaultUpsertsNoDuplicate(t *testing.T) {
	s := openSeededStore(t)
	fid := int64(mapi.PrivateFIDCalendar) // already has a seeded default row

	if err := s.ModifyPermissions(fid, false, []PermissionChange{
		{Op: PermAdd, MemberID: mapi.MemberIDDefault, Rights: mapi.RightsOwner},
	}); err != nil {
		t.Fatal(err)
	}
	m := permByMember(t, s, fid)
	if len(m) != 1 {
		t.Fatalf("Add on Default produced %d rows, want 1 (duplicate default)", len(m))
	}
	if e := m[mapi.MemberIDDefault]; e.Rights != mapi.RightsOwner {
		t.Errorf("default rights = 0x%X, want 0x%X", e.Rights, mapi.RightsOwner)
	}
}

// TestResolvePermissionExactWinsOverDefault is the resolver's load-bearing rule:
// an exact-username grant takes precedence over the "default" member grant, and a
// user with no exact grant falls through to default. Distinct rights make the
// precedence observable — a resolver that returned default for everyone, or
// exact-or-nothing, would fail one of the two assertions.
func TestResolvePermissionExactWinsOverDefault(t *testing.T) {
	s := openSeededStore(t)
	fid := int64(mapi.PrivateFIDInbox)
	if err := s.ModifyPermissions(fid, false, []PermissionChange{
		{Op: PermAdd, Username: "alice@test", Rights: mapi.RightsEditor},
		{Op: PermAdd, MemberID: mapi.MemberIDDefault, Rights: mapi.FrightsFreeBusySimple},
	}); err != nil {
		t.Fatal(err)
	}

	if r, err := s.ResolvePermission(fid, "alice@test"); err != nil || r != mapi.RightsEditor {
		t.Errorf("alice = 0x%X (err %v), want her exact RightsEditor 0x%X", r, err, mapi.RightsEditor)
	}
	if r, err := s.ResolvePermission(fid, "bob@test"); err != nil || r != mapi.FrightsFreeBusySimple {
		t.Errorf("bob = 0x%X (err %v), want the default grant 0x%X", r, err, mapi.FrightsFreeBusySimple)
	}
}

// TestResolvePermissionNoDefaultIsNone confirms a user with neither an exact grant
// nor a "default" member grant resolves to no rights (0), not an error — the inert
// state a seeded folder without a default has.
func TestResolvePermissionNoDefaultIsNone(t *testing.T) {
	s := openSeededStore(t)
	fid := int64(mapi.PrivateFIDInbox)
	if err := s.ModifyPermissions(fid, false, []PermissionChange{
		{Op: PermAdd, Username: "alice@test", Rights: mapi.RightsEditor},
	}); err != nil {
		t.Fatal(err)
	}
	if r, err := s.ResolvePermission(fid, "bob@test"); err != nil || r != 0 {
		t.Errorf("bob with no default = 0x%X (err %v), want 0 (no rights)", r, err)
	}
}

// TestResolvePermissionAnonymous confirms an anonymous caller (empty username)
// matches the stored anonymous ("") row through the exact lookup, ahead of the
// "default" member.
func TestResolvePermissionAnonymous(t *testing.T) {
	s := openSeededStore(t)
	fid := int64(mapi.PrivateFIDInbox)
	if err := s.ModifyPermissions(fid, false, []PermissionChange{
		{Op: PermAdd, MemberID: mapi.MemberIDAnonymous, Rights: mapi.FrightsFreeBusySimple},
		{Op: PermAdd, MemberID: mapi.MemberIDDefault, Rights: mapi.RightsReviewer},
	}); err != nil {
		t.Fatal(err)
	}
	if r, err := s.ResolvePermission(fid, ""); err != nil || r != mapi.FrightsFreeBusySimple {
		t.Errorf("anonymous = 0x%X (err %v), want the anonymous grant 0x%X", r, err, mapi.FrightsFreeBusySimple)
	}
}
