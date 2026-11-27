package directory

import (
	"testing"
	"time"
)

func setupAntispamSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	res, err := db.Exec("DELETE FROM antispam_settings")
	mustNoErr(t, "clear the antispam settings", err)
	cleared, err := res.RowsAffected()
	mustNoErr(t, "count the cleared rows", err)
	wantEq(t, "at most the one settings row was cleared", cleared <= 1, true)
	return d
}

// TestAntispamSettingsRoundTrip proves a fresh database reports no settings (so
// the caller seeds defaults), and that a saved row reads back with its fields and
// a version stamp.
func TestAntispamSettingsRoundTrip(t *testing.T) {
	d := setupAntispamSettings(t)

	if empty, found, err := d.GetAntispamSettings(); err != nil || found || empty != (AntispamSettings{}) {
		t.Fatalf("Get on empty = %+v found %v err %v, want zero/not found", empty, found, err)
	}

	want := AntispamSettings{SPFFail: 5, SPFSoftFail: 2, DKIMFail: 3, DMARCFail: 6, DNSBLHit: 6, BayesSpam: 4, SARulesHit: 4, Threshold: 8, Zones: "zen.example,bl.example", BayesProb: 0.9, SAThreshold: 4.5}
	before := time.Now().UnixMilli()
	if err := d.SetAntispamSettings(want); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UnixMilli()
	got, found, err := d.GetAntispamSettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	// The version stamp is the save time in Unix milliseconds.
	if got.UpdatedAt < before || got.UpdatedAt > after {
		t.Errorf("UpdatedAt = %d, want the save time in [%d, %d]", got.UpdatedAt, before, after)
	}
	got.UpdatedAt = 0
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
}

// TestAntispamSettingsUpsert proves a second save replaces the single row (not a
// second row) and advances the version stamp.
func TestAntispamSettingsUpsert(t *testing.T) {
	d := setupAntispamSettings(t)
	if err := d.SetAntispamSettings(AntispamSettings{Threshold: 8, Zones: "a.example"}); err != nil {
		t.Fatal(err)
	}
	first, found, err := d.GetAntispamSettings()
	if err != nil || !found {
		t.Fatalf("Get first = found %v err %v", found, err)
	}

	if err := d.SetAntispamSettings(AntispamSettings{Threshold: 12, Zones: "b.example"}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetAntispamSettings()
	if err != nil || !found {
		t.Fatalf("Get second = found %v err %v", found, err)
	}
	if got.Threshold != 12 || got.Zones != "b.example" {
		t.Errorf("after upsert = threshold %d zones %q, want 12 / b.example", got.Threshold, got.Zones)
	}
	if got.UpdatedAt < first.UpdatedAt {
		t.Errorf("UpdatedAt went backwards: %d then %d", first.UpdatedAt, got.UpdatedAt)
	}
	wantRows(t, d.db, "settings rows after two saves", 1, `SELECT COUNT(*) FROM antispam_settings`)
}
