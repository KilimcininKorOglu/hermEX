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

	rooms, err := d.ListRooms()
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
	if _, err := d.CreateRoom("conf-a@hermex.test", "Conference A", filepath.Join(root, "conf-a"), 8, false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateRoom("projector-1@hermex.test", "Projector", filepath.Join(root, "proj"), 0, true); err != nil {
		t.Fatal(err)
	}
	// A room must belong to a known domain.
	if _, err := d.CreateRoom("ghost@nope.test", "Ghost", filepath.Join(root, "ghost"), 0, false); err == nil {
		t.Error("CreateRoom into an unknown domain should fail")
	}

	rooms, err := d.ListRooms()
	if err != nil {
		t.Fatal(err)
	}
	byAddr := map[string]GALEntry{}
	for _, r := range rooms {
		byAddr[r.Address] = r
	}
	conf, ok := byAddr["conf-a@hermex.test"]
	if !ok || conf.DisplayName != "Conference A" || conf.Capacity != 8 || conf.DisplayType != dtRoom {
		t.Errorf("conference room = %+v, want name=Conference A capacity=8 DT_ROOM", conf)
	}
	proj, ok := byAddr["projector-1@hermex.test"]
	if !ok || proj.DisplayName != "Projector" || proj.Capacity != 0 || proj.DisplayType != dtEquipment {
		t.Errorf("equipment = %+v, want name=Projector capacity=0 DT_EQUIPMENT", proj)
	}

	// The resource cannot sign in: no password is stored.
	var pw string
	if err := db.QueryRow("SELECT password FROM users WHERE username = ?", "conf-a@hermex.test").Scan(&pw); err != nil {
		t.Fatal(err)
	}
	if pw != "" {
		t.Errorf("room password = %q, want empty (a resource cannot sign in)", pw)
	}
}
