package directory

import "testing"

func setupMessageSizeSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM message_size_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestMessageSizeSettingsRoundTrip proves a fresh database reports no settings (so the
// MTA keeps the server's no-limit default), and that a saved limit reads back.
func TestMessageSizeSettingsRoundTrip(t *testing.T) {
	d := setupMessageSizeSettings(t)

	if _, found, err := d.GetMessageSizeSettings(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
	}

	want := MessageSizeSettings{MaxInboundBytes: 26214400} // 25 MiB
	if err := d.SetMessageSizeSettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetMessageSizeSettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
}

// TestMessageSizeSettingsUpsert proves a second save replaces the single row.
func TestMessageSizeSettingsUpsert(t *testing.T) {
	d := setupMessageSizeSettings(t)
	if err := d.SetMessageSizeSettings(MessageSizeSettings{MaxInboundBytes: 10485760}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMessageSizeSettings(MessageSizeSettings{MaxInboundBytes: 52428800}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetMessageSizeSettings()
	if err != nil || !found {
		t.Fatalf("Get after upsert = found %v err %v", found, err)
	}
	if got.MaxInboundBytes != 52428800 {
		t.Errorf("after upsert = %+v, want 52428800", got)
	}
}
