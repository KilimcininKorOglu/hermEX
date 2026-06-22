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
	if _, err := db.Exec("DELETE FROM rate_limit_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestRateLimitSettingsRoundTrip proves a fresh database reports no settings (so the
// MTA keeps the limiter disabled with its built-in defaults), and that a saved row
// reads back field for field.
func TestRateLimitSettingsRoundTrip(t *testing.T) {
	d := setupRateLimitSettings(t)

	if _, found, err := d.GetRateLimitSettings(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
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
}
