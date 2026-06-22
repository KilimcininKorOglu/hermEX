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

// TestGreylistTimingsRoundTrip proves the timings report unsaved on a fresh database
// (so the caller keeps the greylister's built-in defaults), persist when saved, and
// are orthogonal to the enable toggle: enabling does not reset saved timings, and
// saving timings does not flip the enable state — the two share one row but are edited
// by separate partial upserts.
func TestGreylistTimingsRoundTrip(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM greylist_settings"); err != nil {
		t.Fatal(err)
	}

	if _, found, err := d.GetGreylistTimings(); err != nil || found {
		t.Fatalf("Get on empty = found %v err %v, want not found", found, err)
	}

	want := GreylistTimings{MinDelay: 600, UnconfirmedTTL: 7200, ConfirmedTTL: 1000000}
	if err := d.SetGreylistTimings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetGreylistTimings()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if got != want {
		t.Errorf("timings = %+v, want %+v", got, want)
	}

	// Enabling must not reset the saved timings.
	if err := d.SetGreylistEnabled(true); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := d.GetGreylistTimings(); got != want {
		t.Errorf("timings clobbered by enable: %+v, want %+v", got, want)
	}
	// And saving timings must not flip the enable state.
	if err := d.SetGreylistTimings(GreylistTimings{MinDelay: 300, UnconfirmedTTL: 86400, ConfirmedTTL: 3110400}); err != nil {
		t.Fatal(err)
	}
	if on, _ := d.GetGreylistEnabled(); !on {
		t.Error("enable state cleared by saving timings, want still on")
	}
}
