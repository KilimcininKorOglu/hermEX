package directory

import "testing"

func setupRelaySettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM relay_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestRelaySettingsRoundTrip proves a fresh database reports no settings (so the MTA
// keeps the relay worker's built-in defaults), and that a saved row reads back.
func TestRelaySettingsRoundTrip(t *testing.T) {
	d := setupRelaySettings(t)

	if _, found, err := d.GetRelaySettings(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
	}

	want := RelaySettings{BackoffSeconds: 120, MaxAttempts: 5}
	if err := d.SetRelaySettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetRelaySettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
}

// TestRelaySettingsUpsert proves a second save replaces the single row.
func TestRelaySettingsUpsert(t *testing.T) {
	d := setupRelaySettings(t)
	if err := d.SetRelaySettings(RelaySettings{BackoffSeconds: 300, MaxAttempts: 10}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRelaySettings(RelaySettings{BackoffSeconds: 60, MaxAttempts: 20}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetRelaySettings()
	if err != nil || !found {
		t.Fatalf("Get after upsert = found %v err %v", found, err)
	}
	if got.BackoffSeconds != 60 || got.MaxAttempts != 20 {
		t.Errorf("after upsert = %+v, want 60 / 20", got)
	}
}
