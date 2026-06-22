package directory

import "testing"

func setupSizeLimits(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM size_limits"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestSizeLimitsRoundTrip proves a fresh database reports no limits (so each protocol
// keeps its built-in default), and that a saved row reads back.
func TestSizeLimitsRoundTrip(t *testing.T) {
	d := setupSizeLimits(t)

	if _, found, err := d.GetSizeLimits(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
	}

	want := SizeLimits{IMAPLiteralBytes: 10485760, EWSRequestBytes: 4194304} // 10 MiB / 4 MiB
	if err := d.SetSizeLimits(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetSizeLimits()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if got != want {
		t.Errorf("limits = %+v, want %+v", got, want)
	}
}

// TestSizeLimitsUpsert proves a second save replaces the single row.
func TestSizeLimitsUpsert(t *testing.T) {
	d := setupSizeLimits(t)
	if err := d.SetSizeLimits(SizeLimits{IMAPLiteralBytes: 52428800, EWSRequestBytes: 8388608}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSizeLimits(SizeLimits{IMAPLiteralBytes: 1048576, EWSRequestBytes: 2097152}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetSizeLimits()
	if err != nil || !found {
		t.Fatalf("Get after upsert = found %v err %v", found, err)
	}
	if got.IMAPLiteralBytes != 1048576 || got.EWSRequestBytes != 2097152 {
		t.Errorf("after upsert = %+v, want 1048576 / 2097152", got)
	}
}
