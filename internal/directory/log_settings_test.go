package directory

import "testing"

func setupLogSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM log_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestLogRetentionRoundTrip proves a fresh database reports no retention row (so the
// admin keeps its config seed), and that a saved window reads back.
func TestLogRetentionRoundTrip(t *testing.T) {
	d := setupLogSettings(t)

	if _, found, err := d.GetLogRetentionDays(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
	}

	if err := d.SetLogRetentionDays(90); err != nil {
		t.Fatal(err)
	}
	days, found, err := d.GetLogRetentionDays()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if days != 90 {
		t.Errorf("retention = %d, want 90", days)
	}
}

// TestLogSpillLimitIsItsOwnSetting proves the spill cap reads its default before
// any save, reads back what was saved, refuses a negative cap, and that saving
// the retention window and the cap never overwrite each other in the shared row.
func TestLogSpillLimitIsItsOwnSetting(t *testing.T) {
	d := setupLogSettings(t)
	wantSpillLimit(t, d, DefaultLogSpillLimitMB, "the cap on an empty table")
	mustNoErr(t, "save the retention", d.SetLogRetentionDays(30))
	wantSpillLimit(t, d, DefaultLogSpillLimitMB, "the cap after saving the retention")
	mustNoErr(t, "save the cap", d.SetLogSpillLimitMB(200))
	wantSpillLimit(t, d, 200, "the saved cap")
	days, _, err := d.GetLogRetentionDays()
	mustNoErr(t, "read the retention", err)
	if days != 30 {
		t.Errorf("saving the cap changed retention to %d, want 30", days)
	}
	if err := d.SetLogSpillLimitMB(-1); err == nil {
		t.Error("a negative cap was accepted")
	}
	mustNoErr(t, "save no cap", d.SetLogSpillLimitMB(0))
	wantSpillLimit(t, d, 0, "the cap after saving 0 (no cap)")
}

// wantSpillLimit reads the spill cap and checks it.
func wantSpillLimit(t *testing.T, d *SQLDirectory, want int, what string) {
	t.Helper()
	mb, err := d.GetLogSpillLimitMB()
	mustNoErr(t, "read the cap", err)
	if mb != want {
		t.Errorf("%s = %d, want %d", what, mb, want)
	}
}

// TestLogRetentionUpsert proves a second save replaces the single row, including the
// keep-forever value (0) an operator may set to disable pruning.
func TestLogRetentionUpsert(t *testing.T) {
	d := setupLogSettings(t)
	if err := d.SetLogRetentionDays(30); err != nil {
		t.Fatal(err)
	}
	if err := d.SetLogRetentionDays(0); err != nil {
		t.Fatal(err)
	}
	days, found, err := d.GetLogRetentionDays()
	if err != nil || !found {
		t.Fatalf("Get after upsert = found %v err %v", found, err)
	}
	if days != 0 {
		t.Errorf("after upsert = %d, want 0 (keep forever)", days)
	}
}
