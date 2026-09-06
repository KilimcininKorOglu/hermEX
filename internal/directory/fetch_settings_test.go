package directory

import "testing"

// TestFetchSettingsRoundTrip proves a fresh database reports no policy (so the
// worker keeps refusing internal sources) and that a saved row reads back. The
// default matters as much as the round trip: an install that never opens the page
// must not fetch from internal addresses.
func TestFetchSettingsRoundTrip(t *testing.T) {
	d, db := freshDirectory(t)
	_, err := db.Exec("DELETE FROM fetch_settings")
	mustNoErr(t, "clear the settings row", err)
	read := func() (FetchSettings, bool) {
		t.Helper()
		s, found, err := d.GetFetchSettings()
		mustNoErr(t, "get the fetch settings", err)
		return s, found
	}

	s, found := read()
	wantEq(t, "a row is found on an empty table", found, false)
	wantEq(t, "internal sources allowed by default", s.AllowInternalSources, false)

	mustNoErr(t, "allow internal sources", d.SetFetchSettings(FetchSettings{AllowInternalSources: true}))
	s, found = read()
	wantEq(t, "the row is found after the set", found, true)
	wantEq(t, "internal sources allowed after the set", s.AllowInternalSources, true)

	// Turning it back off upserts the same row rather than adding another.
	mustNoErr(t, "refuse internal sources", d.SetFetchSettings(FetchSettings{}))
	s, _ = read()
	wantEq(t, "internal sources allowed after being turned off", s.AllowInternalSources, false)
	wantRows(t, db, "fetch_settings rows (it is a single-row table)", 1, "SELECT COUNT(*) FROM fetch_settings")
}
