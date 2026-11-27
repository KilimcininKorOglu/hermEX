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
	res, err := db.Exec("DELETE FROM message_size_settings")
	mustNoErr(t, "clear the message size settings", err)
	cleared, err := res.RowsAffected()
	mustNoErr(t, "count the cleared rows", err)
	wantEq(t, "at most the one settings row was cleared", cleared <= 1, true)
	return d
}

// TestMessageSizeSettingsRoundTrip proves a fresh database reports no settings (so the
// MTA keeps DefaultMaxInboundBytes), and that a saved limit reads back.
func TestMessageSizeSettingsRoundTrip(t *testing.T) {
	d := setupMessageSizeSettings(t)

	if empty, found, err := d.GetMessageSizeSettings(); err != nil || found || empty != (MessageSizeSettings{}) {
		t.Fatalf("Get on empty = %+v found %v err %v, want zero not found", empty, found, err)
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
	wantRows(t, d.db, "settings rows after two saves", 1, `SELECT COUNT(*) FROM message_size_settings`)
}

// TestMessageSizeZeroIsAStoredChoice proves a saved 0 (no limit) reads back as a
// stored row, so it is told apart from the unsaved state that falls back to
// DefaultMaxInboundBytes.
func TestMessageSizeZeroIsAStoredChoice(t *testing.T) {
	d := setupMessageSizeSettings(t)
	mustNoErr(t, "save a limit", d.SetMessageSizeSettings(MessageSizeSettings{MaxInboundBytes: 10485760}))
	mustNoErr(t, "save no limit", d.SetMessageSizeSettings(MessageSizeSettings{MaxInboundBytes: 0}))
	got, found, err := d.GetMessageSizeSettings()
	mustNoErr(t, "read the settings", err)
	wantEq(t, "the no-limit row is stored", found, true)
	wantEq(t, "the stored limit", got.MaxInboundBytes, int64(0))
}
