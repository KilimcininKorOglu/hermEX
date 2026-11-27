package directory

import "testing"

// TestUserZoneReadsTheStoredTimezone proves the inbound calendar paths can learn
// the wall clock a floating iCalendar time means. Such a value carries no zone
// and is defined as the reader's own local time, so without this lookup a
// delivered invitation lands wrong by the mailbox owner's offset.
func TestUserZoneReadsTheStoredTimezone(t *testing.T) {
	d := freshSQL(t)
	root := t.TempDir()
	seedDomain(t, d, root, "acme.test")
	seedUser(t, d, root, "u@acme.test", "password1")

	if loc := UserZone(d, "u@acme.test"); loc != nil {
		t.Fatalf("a user with no stored timezone answered %v, want no zone", loc)
	}

	if ok, err := d.SetUserLocale("u@acme.test", "Europe/Istanbul", "tr"); err != nil || !ok {
		t.Fatalf("set user locale: %v %v", ok, err)
	}
	loc := UserZone(d, "u@acme.test")
	if loc == nil || loc.String() != "Europe/Istanbul" {
		t.Fatalf("zone = %v, want Europe/Istanbul", loc)
	}
}

// TestUserZoneFailsSoft covers every answer that is not a zone: an unknown user,
// a stored name that is not an IANA zone, and a directory with no such capability
// at all. Each one means the caller reads the value as UTC and records that it
// did, never that delivery fails.
func TestUserZoneFailsSoft(t *testing.T) {
	d := freshSQL(t)
	root := t.TempDir()
	seedDomain(t, d, root, "acme.test")
	seedUser(t, d, root, "u@acme.test", "password1")

	if loc := UserZone(d, "nobody@acme.test"); loc != nil {
		t.Errorf("an unknown user answered %v, want no zone", loc)
	}
	// SetUserLocale refuses such names, so rows written before that check are
	// planted directly: an unknown zone and a path out of the zone database.
	for _, bad := range []string{"Mars/Olympus", "../../../../etc/localtime"} {
		res, err := d.db.Exec(`UPDATE users SET timezone = ? WHERE username = 'u@acme.test'`, bad)
		mustNoErr(t, "plant an unloadable zone", err)
		n, err := res.RowsAffected()
		mustNoErr(t, "count the planted row", err)
		wantEq(t, "rows planted with "+bad, n, int64(1))
		if loc := UserZone(d, "u@acme.test"); loc != nil {
			t.Errorf("the zone name %q answered %v, want no zone", bad, loc)
		}
	}
	if loc := UserZone(StaticAccounts{}, "u@acme.test"); loc != nil {
		t.Errorf("a directory without the capability answered %v, want no zone", loc)
	}
}
