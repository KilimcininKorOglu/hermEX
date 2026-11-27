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
	res, err := db.Exec("DELETE FROM spam_history_settings")
	mustNoErr(t, "clear the spam history settings", err)
	cleared, err := res.RowsAffected()
	mustNoErr(t, "count the cleared rows", err)
	wantEq(t, "at most the one settings row was cleared", cleared <= 1, true)
	return d
}

// TestSpamHistorySettingsRoundTrip proves a fresh database reports no settings (so the
// MTA keeps the built-in retention default), and that a saved row reads back.
func TestSpamHistorySettingsRoundTrip(t *testing.T) {
	d := setupSpamHistorySettings(t)

	if empty, found, err := d.GetSpamHistorySettings(); err != nil || found || empty != (SpamHistorySettings{}) {
		t.Fatalf("Get on empty = %+v found %v err %v, want zero not found", empty, found, err)
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
	wantRows(t, d.db, "settings rows after two saves", 1, `SELECT COUNT(*) FROM spam_history_settings`)
}

// TestSpamHistorySettingsRefuseANonPositiveRetain proves the stored setting holds
// the same guard as the runtime bound: a value below 1 is refused and the stored
// row kept, so the next poll cannot load a value that prunes every row.
func TestSpamHistorySettingsRefuseANonPositiveRetain(t *testing.T) {
	d := setupSpamHistorySettings(t)
	mustNoErr(t, "save the retention", d.SetSpamHistorySettings(SpamHistorySettings{Retain: 500}))
	for _, bad := range []int{0, -1} {
		wantErr(t, "a non-positive retention was accepted", d.SetSpamHistorySettings(SpamHistorySettings{Retain: bad}))
		got, found, err := d.GetSpamHistorySettings()
		mustNoErr(t, "read the retention after a refusal", err)
		wantEq(t, "retention found after a refusal", found, true)
		wantEq(t, "retention after a refusal", got.Retain, 500)
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
	d.SetSpamHistoryRetain(-7)
	if got := d.spamRetain.Load(); got != 42 {
		t.Errorf("retain after guarded set = %d, want unchanged 42", got)
	}
}
