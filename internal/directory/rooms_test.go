package directory

import (
	"path/filepath"
	"testing"
)

// TestListRooms proves the room picker query returns only resource mailboxes
// (DT_ROOM/DT_EQUIPMENT), not ordinary users, so the picker is not polluted with
// every mailbox.
func TestListRooms(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "dom")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("alice@hermex.test", "secret", filepath.Join(root, "alice")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("boardroom@hermex.test", "secret", filepath.Join(root, "room")); err != nil {
		t.Fatal(err)
	}
	// Promote the boardroom mailbox to a DT_ROOM resource.
	if _, err := db.Exec("UPDATE users SET display_type = ? WHERE username = ?", dtRoom, "boardroom@hermex.test"); err != nil {
		t.Fatal(err)
	}

	rooms, err := d.ListRooms("alice@hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 {
		t.Fatalf("ListRooms returned %d entries, want 1 (the room only, not alice)", len(rooms))
	}
	if rooms[0].Address != "boardroom@hermex.test" || rooms[0].DisplayType != dtRoom {
		t.Errorf("room = %+v, want boardroom@hermex.test DT_ROOM", rooms[0])
	}
}

// TestCreateRoom proves CreateRoom provisions a bookable resource the picker then
// lists with its display name and seating capacity, that equipment is distinguished
// from a room by display_type, that an unknown domain is rejected, and that the
// resource carries no password so it cannot sign in.
func TestCreateRoom(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	// The room list is scoped to the caller, so the domain needs an account to
	// ask as; a room is looked up on behalf of a person, never on its own.
	mustCreateUser(t, d, root, "alice@hermex.test", "secret")
	_, err := d.CreateRoom("conf-a@hermex.test", "Conference A", filepath.Join(root, "conf-a"), 8, false)
	mustNoErr(t, "create the conference room", err)
	_, err = d.CreateRoom("projector-1@hermex.test", "Projector", filepath.Join(root, "proj"), 0, true)
	mustNoErr(t, "create the equipment", err)
	// A room must belong to a known domain.
	_, err = d.CreateRoom("ghost@nope.test", "Ghost", filepath.Join(root, "ghost"), 0, false)
	wantErr(t, "CreateRoom into an unknown domain succeeded", err)

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
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "dom")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("alice@hermex.test", "secret", filepath.Join(root, "alice")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateRoom("conf-a@hermex.test", "Conference A", filepath.Join(root, "conf"), 8, false); err != nil {
		t.Fatal(err)
	}
	entries, err := d.SearchGAL("alice@hermex.test", "conf-a", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Address != "conf-a@hermex.test" || entries[0].Capacity != 8 {
		t.Errorf("SearchGAL room = %+v, want conf-a@hermex.test with Capacity 8", entries)
	}
}
