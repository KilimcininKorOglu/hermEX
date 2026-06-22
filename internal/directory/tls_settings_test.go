package directory

import "testing"

func setupTLSSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM tls_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestTLSSettingsDefaultsToManual proves an unconfigured deployment reads as manual
// mode rather than erroring — an upgrade must never silently flip a running server
// into reaching out to a CA, so the absent row is the safe default.
func TestTLSSettingsDefaultsToManual(t *testing.T) {
	d := setupTLSSettings(t)
	s, found, err := d.GetTLSSettings()
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("found = true on an empty table; want false")
	}
	if s.Mode != "manual" {
		t.Errorf("default mode = %q, want manual", s.Mode)
	}
}

// TestTLSSettingsRoundTripAndUpsert proves the ACME configuration round-trips —
// including the agreed flag — and that saving again replaces the single row rather
// than adding one, so the gateway always reads exactly one current mode.
func TestTLSSettingsRoundTripAndUpsert(t *testing.T) {
	d := setupTLSSettings(t)
	want := TLSSettings{Mode: "acme", ACMEEmail: "ops@example.com", ACMECAURL: "https://acme-staging.example/dir", ACMEAgreed: true}
	if err := d.SetTLSSettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetTLSSettings()
	if err != nil || !found {
		t.Fatalf("GetTLSSettings found=%v err=%v; want a saved row", found, err)
	}
	if got != want {
		t.Errorf("round-trip = %+v, want %+v", got, want)
	}

	// Re-save with manual mode and ToS withdrawn: a replacement, not a second row.
	if err := d.SetTLSSettings(TLSSettings{Mode: "manual"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = d.GetTLSSettings()
	if got.Mode != "manual" || got.ACMEAgreed {
		t.Errorf("after re-save = %+v, want manual mode with agreed=false", got)
	}
	var n int
	if err := d.db.QueryRow("SELECT COUNT(*) FROM tls_settings").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("tls_settings has %d rows after two saves, want exactly 1", n)
	}
}
