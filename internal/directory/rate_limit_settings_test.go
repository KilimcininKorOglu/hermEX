package directory

import "testing"

func setupRateLimitSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	res, err := db.Exec("DELETE FROM rate_limit_settings")
	mustNoErr(t, "clear the rate limit settings", err)
	cleared, err := res.RowsAffected()
	mustNoErr(t, "count the cleared rows", err)
	wantEq(t, "at most the one settings row was cleared", cleared <= 1, true)
	return d
}

// TestRateLimitSettingsRoundTrip proves a fresh database reports no settings (so the
// MTA keeps the limiter disabled with its built-in defaults), and that a saved row
// reads back field for field.
func TestRateLimitSettingsRoundTrip(t *testing.T) {
	d := setupRateLimitSettings(t)

	if empty, found, err := d.GetRateLimitSettings(); err != nil || found || empty != (RateLimitSettings{}) {
		t.Fatalf("Get on empty = %+v found %v err %v, want zero not found", empty, found, err)
	}

	want := RateLimitSettings{Enabled: true, Burst: 120, WindowSeconds: 30}
	if err := d.SetRateLimitSettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetRateLimitSettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
}

// TestRateLimitSettingsRefuseANonPositiveLimit proves a burst or window below 1 is
// refused and the stored row is kept.
func TestRateLimitSettingsRefuseANonPositiveLimit(t *testing.T) {
	d := setupRateLimitSettings(t)
	want := RateLimitSettings{Enabled: true, Burst: 120, WindowSeconds: 30}
	mustNoErr(t, "save the starting settings", d.SetRateLimitSettings(want))
	for _, bad := range []RateLimitSettings{
		{Enabled: true, Burst: 0, WindowSeconds: 30},
		{Enabled: true, Burst: -5, WindowSeconds: 30},
		{Enabled: true, Burst: 120, WindowSeconds: 0},
		{Enabled: true, Burst: 120, WindowSeconds: -1},
	} {
		wantErr(t, "SetRateLimitSettings accepted a non-positive limit", d.SetRateLimitSettings(bad))
		got, found, err := d.GetRateLimitSettings()
		mustNoErr(t, "read the settings after a refusal", err)
		wantEq(t, "settings found after a refusal", found, true)
		wantEq(t, "settings after a refusal", got, want)
	}
}

// TestRateLimitSettingsUpsert proves a second save replaces the single row rather
// than inserting a second.
func TestRateLimitSettingsUpsert(t *testing.T) {
	d := setupRateLimitSettings(t)
	if err := d.SetRateLimitSettings(RateLimitSettings{Enabled: true, Burst: 60, WindowSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRateLimitSettings(RateLimitSettings{Enabled: false, Burst: 200, WindowSeconds: 10}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetRateLimitSettings()
	if err != nil || !found {
		t.Fatalf("Get after upsert = found %v err %v", found, err)
	}
	if got.Enabled || got.Burst != 200 || got.WindowSeconds != 10 {
		t.Errorf("after upsert = %+v, want disabled / 200 / 10", got)
	}
	wantRows(t, d.db, "settings rows after two saves", 1, `SELECT COUNT(*) FROM rate_limit_settings`)
}
