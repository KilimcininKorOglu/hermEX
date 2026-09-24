package directory

import "testing"

// TestDMARCReportSettingsRoundTrip proves a fresh database reports no row (so
// reporting stays off), and that switching it on and off again reads back.
func TestDMARCReportSettingsRoundTrip(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	if s, found, err := d.GetDMARCReportSettings(); err != nil || found || s.Enabled {
		t.Fatalf("Get on empty = %+v found %v err %v, want off and not found", s, found, err)
	}
	for _, want := range []bool{true, false} {
		if err := d.SetDMARCReportSettings(DMARCReportSettings{Enabled: want}); err != nil {
			t.Fatal(err)
		}
		got, found, err := d.GetDMARCReportSettings()
		if err != nil || !found || got.Enabled != want {
			t.Fatalf("Get after Set(%v) = %+v found %v err %v", want, got, found, err)
		}
	}
}
