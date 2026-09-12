package directory

import "testing"

// TestUserZoneReadsTheStoredTimezone proves the inbound calendar paths can learn
// the wall clock a floating iCalendar time means. Such a value carries no zone
// and is defined as the reader's own local time, so without this lookup a
// delivered invitation lands wrong by the mailbox owner's offset.
func TestUserZoneReadsTheStoredTimezone(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "acme.test")
	mustCreateUser(t, d, root, "u@acme.test", "pw")

	if loc := UserZone(d, "u@acme.test"); loc != nil {
		t.Fatalf("a user with no stored timezone answered %v, want no zone", loc)
	}

	if _, err := d.SetUserLocale("u@acme.test", "Europe/Istanbul", "tr"); err != nil {
		t.Fatalf("set user locale: %v", err)
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
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "acme.test")
	mustCreateUser(t, d, root, "u@acme.test", "pw")

	if loc := UserZone(d, "nobody@acme.test"); loc != nil {
		t.Errorf("an unknown user answered %v, want no zone", loc)
	}
	if _, err := d.SetUserLocale("u@acme.test", "Mars/Olympus", "tr"); err != nil {
		t.Fatalf("set user locale: %v", err)
	}
	if loc := UserZone(d, "u@acme.test"); loc != nil {
		t.Errorf("an unloadable zone name answered %v, want no zone", loc)
	}
	if loc := UserZone(StaticAccounts{}, "u@acme.test"); loc != nil {
		t.Errorf("a directory without the capability answered %v, want no zone", loc)
	}
}
