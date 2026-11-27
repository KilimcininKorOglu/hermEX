package directory

import (
	"database/sql"
	"testing"
)

func setupHTTPRateLimitSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	clearSettingsRow(t, db.Exec, "DELETE FROM http_rate_limit_settings")
	return d
}

// TestHTTPRateLimitSettingsRoundTrip proves a fresh database reports no settings (so
// every HTTP daemon keeps its limiter disabled with the built-in defaults), and that a
// saved row reads back field for field.
func TestHTTPRateLimitSettingsRoundTrip(t *testing.T) {
	d := setupHTTPRateLimitSettings(t)

	if empty, found, err := d.GetHTTPRateLimitSettings(); err != nil || found || empty != (HTTPRateLimitSettings{}) {
		t.Fatalf("Get on empty = %+v found %v err %v, want not found and zero", empty, found, err)
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
	wantRows(t, d.db, "settings rows after two saves", 1, `SELECT COUNT(*) FROM http_rate_limit_settings`)
}

// TestHTTPRateLimitSettingsSeparateFromSMTP proves the HTTP limiter's settings live in
// their own row: saving one does not disturb the inbound-SMTP limiter's settings, so an
// operator tunes the two protocols independently.
func TestHTTPRateLimitSettingsSeparateFromSMTP(t *testing.T) {
	d := setupHTTPRateLimitSettings(t)
	clearSettingsRow(t, d.db.Exec, "DELETE FROM rate_limit_settings")
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

	// And the other way: an SMTP save leaves the HTTP row as it was.
	mustNoErr(t, "save new SMTP settings", d.SetRateLimitSettings(RateLimitSettings{Enabled: false, Burst: 5, WindowSeconds: 5}))
	gotHTTP, found, err := d.GetHTTPRateLimitSettings()
	mustNoErr(t, "read the HTTP settings", err)
	wantEq(t, "HTTP settings found", found, true)
	wantEq(t, "HTTP settings after an SMTP save", gotHTTP, HTTPRateLimitSettings{Enabled: false, Burst: 1200, WindowSeconds: 120})
}

// clearSettingsRow runs a single-row settings table's DELETE and requires it to
// succeed and remove at most that one row.
func clearSettingsRow(t *testing.T, exec func(string, ...any) (sql.Result, error), stmt string) {
	t.Helper()
	res, err := exec(stmt)
	mustNoErr(t, stmt, err)
	n, err := res.RowsAffected()
	mustNoErr(t, "count the rows of "+stmt, err)
	wantEq(t, "rows removed by "+stmt+" are at most one", n <= 1, true)
}
