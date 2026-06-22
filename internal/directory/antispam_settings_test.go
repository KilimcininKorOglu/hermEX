package directory

import "testing"

func setupAntispamSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM antispam_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestAntispamSettingsRoundTrip proves a fresh database reports no settings (so
// the caller seeds defaults), and that a saved row reads back with its fields and
// a version stamp.
func TestAntispamSettingsRoundTrip(t *testing.T) {
	d := setupAntispamSettings(t)

	if _, found, err := d.GetAntispamSettings(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
	}

	want := AntispamSettings{SPFFail: 5, SPFSoftFail: 2, DKIMFail: 3, DMARCFail: 6, DNSBLHit: 6, BayesSpam: 4, SARulesHit: 4, Threshold: 8, Zones: "zen.example,bl.example"}
	if err := d.SetAntispamSettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetAntispamSettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	want.UpdatedAt = got.UpdatedAt // server-stamped
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
	if got.UpdatedAt == 0 {
		t.Error("UpdatedAt was not stamped")
	}
}

// TestAntispamSettingsUpsert proves a second save replaces the single row (not a
// second row) and advances the version stamp.
func TestAntispamSettingsUpsert(t *testing.T) {
	d := setupAntispamSettings(t)
	if err := d.SetAntispamSettings(AntispamSettings{Threshold: 8, Zones: "a.example"}); err != nil {
		t.Fatal(err)
	}
	first, _, _ := d.GetAntispamSettings()

	if err := d.SetAntispamSettings(AntispamSettings{Threshold: 12, Zones: "b.example"}); err != nil {
		t.Fatal(err)
	}
	got, _, err := d.GetAntispamSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.Threshold != 12 || got.Zones != "b.example" {
		t.Errorf("after upsert = threshold %d zones %q, want 12 / b.example", got.Threshold, got.Zones)
	}
	if got.UpdatedAt < first.UpdatedAt {
		t.Errorf("UpdatedAt went backwards: %d then %d", first.UpdatedAt, got.UpdatedAt)
	}
}
