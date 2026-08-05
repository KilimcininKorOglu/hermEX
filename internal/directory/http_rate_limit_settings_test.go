package directory

import "testing"

func setupHTTPRateLimitSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM http_rate_limit_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestHTTPRateLimitSettingsRoundTrip proves a fresh database reports no settings (so
// every HTTP daemon keeps its limiter disabled with the built-in defaults), and that a
// saved row reads back field for field.
func TestHTTPRateLimitSettingsRoundTrip(t *testing.T) {
	d := setupHTTPRateLimitSettings(t)

	if _, found, err := d.GetHTTPRateLimitSettings(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
	}

	want := HTTPRateLimitSettings{Enabled: true, Burst: 900, WindowSeconds: 30}
	if err := d.SetHTTPRateLimitSettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetHTTPRateLimitSettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
}

// TestHTTPRateLimitSettingsUpsert proves a second save replaces the single row rather
// than inserting a second.
func TestHTTPRateLimitSettingsUpsert(t *testing.T) {
	d := setupHTTPRateLimitSettings(t)
	if err := d.SetHTTPRateLimitSettings(HTTPRateLimitSettings{Enabled: true, Burst: 600, WindowSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetHTTPRateLimitSettings(HTTPRateLimitSettings{Enabled: false, Burst: 120, WindowSeconds: 10}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetHTTPRateLimitSettings()
	if err != nil || !found {
		t.Fatalf("Get after upsert = found %v err %v", found, err)
	}
	if got.Enabled || got.Burst != 120 || got.WindowSeconds != 10 {
		t.Errorf("after upsert = %+v, want disabled / 120 / 10", got)
	}
}

// TestHTTPRateLimitSettingsSeparateFromSMTP proves the HTTP limiter's settings live in
// their own row: saving one does not disturb the inbound-SMTP limiter's settings, so an
// operator tunes the two protocols independently.
func TestHTTPRateLimitSettingsSeparateFromSMTP(t *testing.T) {
	d := setupHTTPRateLimitSettings(t)
	if _, err := d.db.Exec("DELETE FROM rate_limit_settings"); err != nil {
		t.Fatal(err)
	}
	smtp := RateLimitSettings{Enabled: true, Burst: 60, WindowSeconds: 60}
	if err := d.SetRateLimitSettings(smtp); err != nil {
		t.Fatal(err)
	}
	if err := d.SetHTTPRateLimitSettings(HTTPRateLimitSettings{Enabled: false, Burst: 1200, WindowSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetRateLimitSettings()
	if err != nil || !found {
		t.Fatalf("SMTP Get = found %v err %v, want found", found, err)
	}
	if got != smtp {
		t.Errorf("SMTP settings = %+v, want %+v (unchanged)", got, smtp)
	}
}
