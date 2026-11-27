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
	res, err := db.Exec("DELETE FROM relay_settings")
	mustNoErr(t, "clear the relay settings", err)
	cleared, err := res.RowsAffected()
	mustNoErr(t, "count the cleared rows", err)
	wantEq(t, "at most the one settings row was cleared", cleared <= 1, true)
	return d
}

// TestRelaySettingsRoundTrip proves a fresh database reports no settings (so the MTA
// keeps the relay worker's built-in defaults), and that a saved row reads back.
func TestRelaySettingsRoundTrip(t *testing.T) {
	d := setupRelaySettings(t)

	if empty, found, err := d.GetRelaySettings(); err != nil || found || empty != (RelaySettings{}) {
		t.Fatalf("Get on empty = %+v found %v err %v, want zero not found", empty, found, err)
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

// TestRelaySettingsRefuseANonPositiveValue proves a backoff or attempt count below
// 1 is refused and the stored row kept: zero attempts bounces every relayed message
// at once, and a zero backoff retries in a tight loop.
func TestRelaySettingsRefuseANonPositiveValue(t *testing.T) {
	d := setupRelaySettings(t)
	want := RelaySettings{BackoffSeconds: 120, MaxAttempts: 5}
	mustNoErr(t, "save the starting settings", d.SetRelaySettings(want))
	for _, bad := range []RelaySettings{
		{BackoffSeconds: 0, MaxAttempts: 5},
		{BackoffSeconds: -1, MaxAttempts: 5},
		{BackoffSeconds: 120, MaxAttempts: 0},
		{BackoffSeconds: 120, MaxAttempts: -3},
	} {
		wantErr(t, "SetRelaySettings accepted a non-positive value", d.SetRelaySettings(bad))
		got, found, err := d.GetRelaySettings()
		mustNoErr(t, "read the settings after a refusal", err)
		wantEq(t, "settings found after a refusal", found, true)
		wantEq(t, "settings after a refusal", got, want)
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
	wantRows(t, d.db, "settings rows after two saves", 1, `SELECT COUNT(*) FROM relay_settings`)
}
