package directory

import "testing"

// TestGreylistEnabledRoundTrip proves greylisting defaults off, and that the toggle
// is persisted both ways.
func TestGreylistEnabledRoundTrip(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM greylist_settings"); err != nil {
		t.Fatal(err)
	}

	if on, err := d.GetGreylistEnabled(); err != nil || on {
		t.Fatalf("default = %v err %v, want off", on, err)
	}
	if err := d.SetGreylistEnabled(true); err != nil {
		t.Fatal(err)
	}
	if on, err := d.GetGreylistEnabled(); err != nil || !on {
		t.Fatalf("after enable = %v err %v, want on", on, err)
	}
	if err := d.SetGreylistEnabled(false); err != nil {
		t.Fatal(err)
	}
	if on, err := d.GetGreylistEnabled(); err != nil || on {
		t.Fatalf("after disable = %v err %v, want off", on, err)
	}
}
