package directory

import (
	"path/filepath"
	"testing"
)

// TestListRooms proves the room picker query returns only resource mailboxes
// (DT_ROOM/DT_EQUIPMENT), not ordinary users, so the picker is not polluted with
// every mailbox.
func TestListRooms(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	seedUser(t, d, root, "alice@hermex.test", "secret-pw1")
	seedUser(t, d, root, "boardroom@hermex.test", "secret-pw1")
	// Promote the boardroom mailbox to a DT_ROOM resource.
	execRows(t, "promote boardroom", 1)(db.Exec("UPDATE users SET display_type = ? WHERE username = ?", dtRoom, "boardroom@hermex.test"))

	rooms, err := d.ListRooms("alice@hermex.test")
	mustNoErr(t, "ListRooms", err)
	wantEq(t, "room count", len(rooms), 1)
	wantEq(t, "room address", rooms[0].Address, "boardroom@hermex.test")
	wantEq(t, "room display type", rooms[0].DisplayType, dtRoom)
}

// TestCreateRoom proves CreateRoom provisions a bookable resource the picker then
// lists with its display name and seating capacity, that equipment is distinguished
// from a room by display_type, that an unknown domain is rejected, and that the
// resource carries no password so it cannot sign in.
func TestCreateRoom(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	// The room list is scoped to the caller, so the domain needs an account to
	// ask as; a room is looked up on behalf of a person, never on its own.
	seedUser(t, d, root, "alice@hermex.test", "secret-pw1")
	confID, err := d.CreateRoom("conf-a@hermex.test", "Conference A", filepath.Join(root, "conf-a"), 8, false)
	mustNoErr(t, "create the conference room", err)
	projID, err := d.CreateRoom("projector-1@hermex.test", "Projector", filepath.Join(root, "proj"), 0, true)
	mustNoErr(t, "create the equipment", err)
	wantEq(t, "distinct resource ids", confID > 0 && projID > 0 && confID != projID, true)
	// A negative capacity is refused rather than stored as "not given".
	negID, err := d.CreateRoom("neg@hermex.test", "Neg", filepath.Join(root, "neg"), -3, false)
	wantErr(t, "CreateRoom with a negative capacity succeeded", err)
	wantEq(t, "refused neg capacity room id", negID, int64(0))
	// A room must belong to a known domain.
	ghostID, err := d.CreateRoom("ghost@nope.test", "Ghost", filepath.Join(root, "ghost"), 0, false)
	wantErr(t, "CreateRoom into an unknown domain succeeded", err)
	wantEq(t, "refused ghost domain room id", ghostID, int64(0))
	wantRows(t, db, "rows left by the refused room creates", 0,
		`SELECT COUNT(*) FROM users WHERE username IN (?, ?)`, "neg@hermex.test", "ghost@nope.test")

	rooms, err := d.ListRooms("alice@hermex.test")
	mustNoErr(t, "list rooms", err)
	byAddr := map[string]GALEntry{}
	for _, r := range rooms {
		byAddr[r.Address] = r
	}
	wantResource(t, byAddr, "conf-a@hermex.test", "Conference A", 8, dtRoom)
	wantResource(t, byAddr, "projector-1@hermex.test", "Projector", 0, dtEquipment)

	// The resource cannot sign in: no password is stored.
	var pw string
	mustNoErr(t, "read the room's password",
		db.QueryRow("SELECT password FROM users WHERE username = ?", "conf-a@hermex.test").Scan(&pw))
	wantEq(t, "room password (a resource cannot sign in)", pw, "")
	for _, guess := range []string{"", "x"} {
		path, ok := d.Authenticate("conf-a@hermex.test", guess)
		wantEq(t, "the room signed in with the password "+guess, ok, false)
		wantEq(t, "refused room login maildir empty", path, "")
	}
}

// wantResource checks one listed resource carries its name, capacity and kind.
func wantResource(t *testing.T, byAddr map[string]GALEntry, addr, name string, capacity int, displayType int) {
	t.Helper()
	got, ok := byAddr[addr]
	if !ok {
		t.Fatalf("%s is not in the room list", addr)
	}
	wantEq(t, addr+" display name", got.DisplayName, name)
	wantEq(t, addr+" capacity", got.Capacity, capacity)
	wantEq(t, addr+" display type", got.DisplayType, displayType)
}

// TestSearchGALRoomCapacity proves the GAL enumeration that feeds the NSPI address
// book carries a room's seating capacity, so the address book can advertise
// PR_EMS_AB_ROOM_CAPACITY to Outlook.
func TestSearchGALRoomCapacity(t *testing.T) {
	d := freshSQL(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	seedUser(t, d, root, "alice@hermex.test", "secret-pw1")
	rid, err := d.CreateRoom("conf-a@hermex.test", "Conference A", filepath.Join(root, "conf"), 8, false)
	mustNoErr(t, "CreateRoom", err)
	wantEq(t, "room id > 0", rid > 0, true)
	entries, err := d.SearchGAL("alice@hermex.test", "conf-a", 20, 0)
	mustNoErr(t, "SearchGAL", err)
	wantEq(t, "GAL room entry count", len(entries), 1)
	wantEq(t, "GAL room address", entries[0].Address, "conf-a@hermex.test")
	wantEq(t, "GAL room capacity", entries[0].Capacity, 8)
}

// TestRoomCapacityMustBeAWholeNumber proves PidTagAddressBookRoomCapacity, a
// PtLong, is stored only as a non-negative whole number, and that a stored value
// which is not one is reported by every reader instead of shown as capacity 0.
func TestRoomCapacityMustBeAWholeNumber(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	seedUser(t, d, root, "alice@hermex.test", "password1")
	confID, err := d.CreateRoom("conf-a@hermex.test", "Conference A", filepath.Join(root, "conf"), 8, false)
	mustNoErr(t, "create the room", err)
	wantEq(t, "room id > 0", confID > 0, true)

	for _, bad := range []string{"twelve", "-3", "8.5", "99999999999999999999"} {
		ok, err := d.SetUserProperties("conf-a@hermex.test", map[uint32]string{prRoomCapacity: bad})
		wantErr(t, "capacity "+bad+" was stored", err)
		wantEq(t, "refused capacity write reported found for "+bad, ok, false)
	}
	rooms, err := d.ListAllRooms()
	mustNoErr(t, "list rooms after the refused writes", err)
	wantEq(t, "capacity after the refused writes", rooms[0].Capacity, 8)

	// A row written behind the directory's back is corruption, not "unknown".
	execRows(t, "corrupt the stored capacity", 1)(db.Exec(`UPDATE user_properties SET propval_str = 'twelve' WHERE proptag = ?`, prRoomCapacity))
	allRooms, err := d.ListAllRooms()
	wantErr(t, "ListAllRooms read a non-numeric capacity", err)
	wantEq(t, "corrupt ListAllRooms slice len", len(allRooms), 0)
	userRooms, err := d.ListRooms("alice@hermex.test")
	wantErr(t, "ListRooms read a non-numeric capacity", err)
	wantEq(t, "corrupt ListRooms slice len", len(userRooms), 0)
	galRooms, err := d.SearchGAL("alice@hermex.test", "conf-a", 20, 0)
	wantErr(t, "SearchGAL read a non-numeric capacity", err)
	wantEq(t, "corrupt SearchGAL slice len", len(galRooms), 0)
}
