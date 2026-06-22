package directory

import "testing"

func setupSpamHistorySettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM spam_history_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestSpamHistorySettingsRoundTrip proves a fresh database reports no settings (so the
// MTA keeps the built-in retention default), and that a saved row reads back.
func TestSpamHistorySettingsRoundTrip(t *testing.T) {
	d := setupSpamHistorySettings(t)

	if _, found, err := d.GetSpamHistorySettings(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
	}

	want := SpamHistorySettings{Retain: 500}
	if err := d.SetSpamHistorySettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetSpamHistorySettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
}

// TestSpamHistorySettingsUpsert proves a second save replaces the single row rather
// than inserting a second.
func TestSpamHistorySettingsUpsert(t *testing.T) {
	d := setupSpamHistorySettings(t)
	if err := d.SetSpamHistorySettings(SpamHistorySettings{Retain: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSpamHistorySettings(SpamHistorySettings{Retain: 250}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetSpamHistorySettings()
	if err != nil || !found {
		t.Fatalf("Get after upsert = found %v err %v", found, err)
	}
	if got.Retain != 250 {
		t.Errorf("after upsert = %+v, want retain 250", got)
	}
}

// TestSetSpamHistoryRetainGuard proves a value below 1 is ignored, so a
// misconfiguration can never set the runtime bound to a value that prunes the table
// to nothing on the next insert.
func TestSetSpamHistoryRetainGuard(t *testing.T) {
	// The guard exercises only the in-memory atomic bound, so it needs no database.
	d := NewSQL(nil)
	d.SetSpamHistoryRetain(42)
	d.SetSpamHistoryRetain(0)
	if got := d.spamRetain.Load(); got != 42 {
		t.Errorf("retain after guarded set = %d, want unchanged 42", got)
	}
}
