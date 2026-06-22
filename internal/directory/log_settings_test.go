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
