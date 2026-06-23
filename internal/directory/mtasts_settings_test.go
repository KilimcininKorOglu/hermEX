package directory

import "testing"

func setupMTASTSSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM mtasts_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestMTASTSSettingsDefaultsToDisabled proves an unconfigured deployment reads as
// disabled in testing mode rather than erroring — publishing MTA-STS (and especially
// enforcing TLS on inbound mail) must be a deliberate opt-in, so an upgrade never
// silently starts advertising a policy.
func TestMTASTSSettingsDefaultsToDisabled(t *testing.T) {
	d := setupMTASTSSettings(t)
	s, found, err := d.GetMTASTSSettings()
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("found = true on an empty table; want false")
	}
	if s.Enabled {
		t.Error("default Enabled = true; want false (opt-in)")
	}
	if s.Mode != "testing" {
		t.Errorf("default mode = %q, want testing (safe rollout)", s.Mode)
	}
	if s.MaxAge != MTASTSDefaultMaxAge {
		t.Errorf("default MaxAge = %d, want %d", s.MaxAge, MTASTSDefaultMaxAge)
	}
}

// TestMTASTSSettingsRoundTripAndUpsert proves the publishing settings round-trip and
// that re-saving replaces the single row rather than adding one, so the gateway always
// reads exactly one current policy configuration.
func TestMTASTSSettingsRoundTripAndUpsert(t *testing.T) {
	d := setupMTASTSSettings(t)
	want := MTASTSSettings{Enabled: true, Mode: "enforce", MaxAge: 604800}
	if err := d.SetMTASTSSettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetMTASTSSettings()
	if err != nil || !found {
		t.Fatalf("GetMTASTSSettings found=%v err=%v; want a saved row", found, err)
	}
	if got != want {
		t.Errorf("round-trip = %+v, want %+v", got, want)
	}

	// Re-save disabled in testing mode: a replacement, not a second row.
	if err := d.SetMTASTSSettings(MTASTSSettings{Enabled: false, Mode: "testing", MaxAge: 86400}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = d.GetMTASTSSettings()
	if got.Enabled || got.Mode != "testing" {
		t.Errorf("after re-save = %+v, want disabled testing mode", got)
	}
	var n int
	if err := d.db.QueryRow("SELECT COUNT(*) FROM mtasts_settings").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("mtasts_settings has %d rows after two saves, want exactly 1", n)
	}
}
