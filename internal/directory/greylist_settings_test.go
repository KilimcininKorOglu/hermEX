package directory

import "testing"

// TestGreylistEnabledRoundTrip proves greylisting defaults off, and that the toggle
// is persisted both ways.
func TestGreylistEnabledRoundTrip(t *testing.T) {
	d := freshGreylist(t)

	wantEq(t, "greylisting enabled by default", greylistEnabled(t, d), false)
	mustNoErr(t, "enable greylisting", d.SetGreylistEnabled(true))
	wantEq(t, "greylisting enabled after the toggle", greylistEnabled(t, d), true)
	mustNoErr(t, "disable greylisting", d.SetGreylistEnabled(false))
	wantEq(t, "greylisting enabled after disabling", greylistEnabled(t, d), false)

	// The toggle alone created the row, so its timings are the column defaults,
	// which migration 0016 sets to the greylister's built-in values: 300 s delay,
	// 24 h unconfirmed, 36 d confirmed. A zero default would remove the delay.
	got, found, err := d.GetGreylistTimings()
	mustNoErr(t, "read the timings of a toggle-only row", err)
	wantEq(t, "timings found after the toggle", found, true)
	wantEq(t, "timings of a toggle-only row", got, GreylistTimings{MinDelay: 300, UnconfirmedTTL: 86400, ConfirmedTTL: 3110400})
}

// freshGreylist opens an empty directory with no stored greylist row.
func freshGreylist(t *testing.T) *SQLDirectory {
	t.Helper()
	d, db := freshDirectory(t)
	res, err := db.Exec("DELETE FROM greylist_settings")
	mustNoErr(t, "clear the greylist settings", err)
	cleared, err := res.RowsAffected()
	mustNoErr(t, "count the cleared rows", err)
	wantEq(t, "at most the one settings row was cleared", cleared <= 1, true)
	return d
}

// greylistEnabled reads the toggle.
func greylistEnabled(t *testing.T, d *SQLDirectory) bool {
	t.Helper()
	on, err := d.GetGreylistEnabled()
	mustNoErr(t, "read the greylist toggle", err)
	return on
}

// TestGreylistTimingsRoundTrip proves the timings report unsaved on a fresh database
// (so the caller keeps the greylister's built-in defaults), persist when saved, and
// are orthogonal to the enable toggle: enabling does not reset saved timings, and
// saving timings does not flip the enable state, the two share one row but are edited
// by separate partial upserts.
func TestGreylistTimingsRoundTrip(t *testing.T) {
	d := freshGreylist(t)
	timings := func() (GreylistTimings, bool) {
		t.Helper()
		got, found, err := d.GetGreylistTimings()
		mustNoErr(t, "read the greylist timings", err)
		return got, found
	}

	empty, found := timings()
	wantEq(t, "timings found on an empty table", found, false)
	wantEq(t, "zero timings on an empty table", empty, GreylistTimings{})

	want := GreylistTimings{MinDelay: 600, UnconfirmedTTL: 7200, ConfirmedTTL: 1000000}
	mustNoErr(t, "save the timings", d.SetGreylistTimings(want))
	got, found := timings()
	wantEq(t, "timings found after the save", found, true)
	wantEq(t, "the stored timings", got, want)

	// Enabling must not reset the saved timings.
	mustNoErr(t, "enable greylisting", d.SetGreylistEnabled(true))
	got, found = timings()
	wantEq(t, "timings still found after enabling", found, true)
	wantEq(t, "the timings after enabling (they must not be clobbered)", got, want)
	// And saving timings must not flip the enable state.
	mustNoErr(t, "save new timings",
		d.SetGreylistTimings(GreylistTimings{MinDelay: 300, UnconfirmedTTL: 86400, ConfirmedTTL: 3110400}))
	wantEq(t, "the enable state after saving timings", greylistEnabled(t, d), true)
	wantRows(t, d.db, "settings rows after the partial upserts", 1, `SELECT COUNT(*) FROM greylist_settings`)
}
