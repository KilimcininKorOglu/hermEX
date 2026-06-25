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
