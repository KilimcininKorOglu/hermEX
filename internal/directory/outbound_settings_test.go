package directory

import "testing"

func setupOutboundSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM outbound_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestOutboundSettingsRoundTrip proves a fresh database reports no settings (so the
// MTA keeps the limiter disabled with its built-in defaults), and that a saved row
// reads back field for field.
func TestOutboundSettingsRoundTrip(t *testing.T) {
	d := setupOutboundSettings(t)

	if _, found, err := d.GetOutboundSettings(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
	}

	want := OutboundSettings{Enabled: true, RecipientCap: 250, WindowSeconds: 1800}
	if err := d.SetOutboundSettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetOutboundSettings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
}

// TestOutboundSettingsUpsert proves a second save replaces the single row rather than
// inserting a second.
func TestOutboundSettingsUpsert(t *testing.T) {
	d := setupOutboundSettings(t)
	if err := d.SetOutboundSettings(OutboundSettings{Enabled: true, RecipientCap: 500, WindowSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetOutboundSettings(OutboundSettings{Enabled: false, RecipientCap: 100, WindowSeconds: 600}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetOutboundSettings()
	if err != nil || !found {
		t.Fatalf("Get after upsert = found %v err %v", found, err)
	}
	if got.Enabled || got.RecipientCap != 100 || got.WindowSeconds != 600 {
		t.Errorf("after upsert = %+v, want disabled / 100 / 600", got)
	}
}
