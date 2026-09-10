package directory

import "testing"

func setupConnLimitSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	return d
}

// TestConnLimitSettingsRoundTrip proves a fresh database reports no settings (so
// every daemon keeps its cap disabled with the built-in defaults), and that a saved
// row reads back field for field.
func TestConnLimitSettingsRoundTrip(t *testing.T) {
	d := setupConnLimitSettings(t)

	if _, found, err := d.GetConnLimitSettings(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
	}

	want := ConnLimitSettings{Enabled: true, MaxTotal: 500, MaxPerClient: 10}
	if err := d.SetConnLimitSettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetConnLimitSettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
}

// TestConnLimitSettingsUpsert proves a second save replaces the single row rather
// than inserting a second.
func TestConnLimitSettingsUpsert(t *testing.T) {
	d := setupConnLimitSettings(t)
	if err := d.SetConnLimitSettings(ConnLimitSettings{Enabled: true, MaxTotal: 1000, MaxPerClient: 20}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetConnLimitSettings(ConnLimitSettings{Enabled: false, MaxTotal: 200, MaxPerClient: 4}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetConnLimitSettings()
	if err != nil || !found {
		t.Fatalf("Get after upsert = found %v err %v", found, err)
	}
	if got.Enabled || got.MaxTotal != 200 || got.MaxPerClient != 4 {
		t.Errorf("after upsert = %+v, want disabled / 200 / 4", got)
	}
}
