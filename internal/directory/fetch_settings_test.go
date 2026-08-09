package directory

import "testing"

// TestFetchSettingsRoundTrip proves a fresh database reports no policy (so the
// worker keeps refusing internal sources) and that a saved row reads back. The
// default matters as much as the round trip: an install that never opens the page
// must not fetch from internal addresses.
func TestFetchSettingsRoundTrip(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM fetch_settings"); err != nil {
		t.Fatal(err)
	}

	if s, found, err := d.GetFetchSettings(); err != nil || found || s.AllowInternalSources {
		t.Fatalf("Get on empty = %+v found %v err %v, want not found and refusing", s, found, err)
	}

	if err := d.SetFetchSettings(FetchSettings{AllowInternalSources: true}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetFetchSettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if !got.AllowInternalSources {
		t.Error("the stored policy did not read back as allowed")
	}

	// Turning it back off upserts the same row rather than adding another.
	if err := d.SetFetchSettings(FetchSettings{}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := d.GetFetchSettings(); got.AllowInternalSources {
		t.Error("the policy stayed allowed after being turned off")
	}
	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM fetch_settings").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("fetch_settings holds %d rows, want the single row", rows)
	}
}
