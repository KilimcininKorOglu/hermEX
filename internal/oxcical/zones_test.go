package oxcical

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// zoneCal builds a one-event calendar whose DTSTART carries the given TZID
// parameter (empty for a floating value), optionally preceded by a VTIMEZONE.
func zoneCal(tzidParam, vtimezone string) []byte {
	return fmt.Appendf(nil, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n%sBEGIN:VEVENT\r\nUID:zone-1\r\nSUMMARY:Zone\r\nDTSTART%s:20260811T103000\r\nDTEND%s:20260811T113000\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		vtimezone, tzidParam, tzidParam)
}

// importedStart returns the UTC instant an imported event starts at.
func importedStart(t *testing.T, r *resolver, raw []byte, opt Options) time.Time {
	t.Helper()
	msg, err := Import(raw, opt)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	nt, ok := r.timeVal(msg, mapi.NameAppointmentStartWhole)
	if !ok {
		t.Fatal("imported message carries no start")
	}
	return mapi.NTTimeToUnix(nt).UTC()
}

// zoneOpt returns a resolver and an Options that collects every reported zone
// loss into notes.
func zoneOpt(notes *[]ZoneNote) (*resolver, Options) {
	r := newResolver()
	opt := r.opt()
	opt.OnUnresolvedZone = func(n ZoneNote) { *notes = append(*notes, n) }
	return r, opt
}

// TestImportResolvesWindowsZone is the defect this change fixes: Outlook and
// Exchange write the Windows zone id in TZID, time.LoadLocation knows only IANA
// names, so the wall clock was read as UTC and the appointment was stored an
// offset away from the hour the organizer picked.
func TestImportResolvesWindowsZone(t *testing.T) {
	r := newResolver()
	got := importedStart(t, r, zoneCal(";TZID=W. Europe Standard Time", ""), r.opt())
	want := time.Date(2026, 8, 11, 8, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("start = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestImportResolvesWindowsZoneCaseInsensitively guards the lookup shape: a TZID
// is copied verbatim from whatever wrote it, so its case is not the table's.
func TestImportResolvesWindowsZoneCaseInsensitively(t *testing.T) {
	r := newResolver()
	got := importedStart(t, r, zoneCal(";TZID=TURKEY STANDARD TIME", ""), r.opt())
	want := time.Date(2026, 8, 11, 7, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("start = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestImportResolvesStreamVTimezone covers a zone id no table knows: the stream
// carries its own offset and that offset is used rather than UTC.
func TestImportResolvesStreamVTimezone(t *testing.T) {
	vtz := "BEGIN:VTIMEZONE\r\nTZID:Acme/Campus\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\nTZOFFSETFROM:+0530\r\nTZOFFSETTO:+0530\r\nTZNAME:ACME\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n"
	r := newResolver()
	got := importedStart(t, r, zoneCal(";TZID=Acme/Campus", vtz), r.opt())
	want := time.Date(2026, 8, 11, 5, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("start = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestImportKeepsIANAZone fixes that the named tables are additions, not a
// replacement: an IANA TZID still resolves through time.LoadLocation.
func TestImportKeepsIANAZone(t *testing.T) {
	r := newResolver()
	got := importedStart(t, r, zoneCal(";TZID=Europe/Istanbul", ""), r.opt())
	want := time.Date(2026, 8, 11, 7, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("start = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestImportReportsFloatingTime requires the second half of the change: a time
// read as UTC without saying so is a silent loss, so the import names it.
func TestImportReportsFloatingTime(t *testing.T) {
	var notes []ZoneNote
	_, opt := zoneOpt(&notes)
	if _, err := Import(zoneCal("", ""), opt); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("reported %d times, want 2 (DTSTART and DTEND): %+v", len(notes), notes)
	}
	if notes[0].Property != "DTSTART" || notes[0].TZID != "" || notes[0].Value != "20260811T103000" {
		t.Fatalf("first note = %+v", notes[0])
	}
}

// TestImportReportsUnresolvableZone covers the case the tables cannot answer: the
// zone id is reported verbatim, which is the one fact needed to extend the table.
func TestImportReportsUnresolvableZone(t *testing.T) {
	var notes []ZoneNote
	_, opt := zoneOpt(&notes)
	if _, err := Import(zoneCal(";TZID=Mars/Olympus", ""), opt); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(notes) == 0 {
		t.Fatal("an unresolvable TZID was not reported")
	}
	if notes[0].TZID != "Mars/Olympus" {
		t.Fatalf("note TZID = %q, want Mars/Olympus", notes[0].TZID)
	}
}

// TestImportReportsNothingForResolvedTimes keeps the report free of noise: a zone
// that resolved, and a UTC value, are not losses and must not be reported.
func TestImportReportsNothingForResolvedTimes(t *testing.T) {
	var notes []ZoneNote
	_, opt := zoneOpt(&notes)
	if _, err := Import(zoneCal(";TZID=W. Europe Standard Time", ""), opt); err != nil {
		t.Fatalf("Import: %v", err)
	}
	utc := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:z\r\nDTSTART:20260811T103000Z\r\nDTEND:20260811T113000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	if _, err := Import(utc, opt); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("reported %d resolved times, want 0: %+v", len(notes), notes)
	}
}

// TestImportReportsNothingForVTimezoneRules keeps the report free of a second
// kind of noise: the DTSTART inside a VTIMEZONE rule is floating by definition
// and describes the zone, not an instant in it.
func TestImportReportsNothingForVTimezoneRules(t *testing.T) {
	var notes []ZoneNote
	_, opt := zoneOpt(&notes)
	vtz := "BEGIN:VTIMEZONE\r\nTZID:Acme/Campus\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\nTZOFFSETFROM:+0530\r\nTZOFFSETTO:+0530\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n"
	if _, err := Import(zoneCal(";TZID=Acme/Campus", vtz), opt); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("reported %d times inside a VTIMEZONE, want 0: %+v", len(notes), notes)
	}
}

// TestStreamZoneRefusesTransitioningVTimezone fixes the stated limit: a VTIMEZONE
// whose rules name different offsets describes a transition this package does not
// evaluate, and one fixed offset would be wrong for half the year, so the zone
// stays unresolved instead of being guessed.
func TestStreamZoneRefusesTransitioningVTimezone(t *testing.T) {
	vtz := "BEGIN:VTIMEZONE\r\nTZID:Acme/Shifting\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\nBEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n"
	var notes []ZoneNote
	r, opt := zoneOpt(&notes)
	got := importedStart(t, r, zoneCal(";TZID=Acme/Shifting", vtz), opt)
	if !got.Equal(time.Date(2026, 8, 11, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("start = %s, want the value read as UTC", got.Format(time.RFC3339))
	}
	if _, err := Import(zoneCal(";TZID=Acme/Shifting", vtz), opt); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(notes) == 0 {
		t.Fatal("a VTIMEZONE that could not be resolved was not reported")
	}
}

// TestWindowsZonesLoad requires every mapped IANA name to load, so a typo in the
// table fails here instead of shifting an appointment by an hour in production.
func TestWindowsZonesLoad(t *testing.T) {
	for win, iana := range windowsZones {
		if win != strings.ToLower(win) {
			t.Errorf("key %q is not lower-case, so the lookup can never hit it", win)
		}
		if _, err := time.LoadLocation(iana); err != nil {
			t.Errorf("%s -> %s: %v", win, iana, err)
		}
	}
}

// TestParseUTCOffset covers the VTIMEZONE offset forms and the values that must
// be refused rather than read as a partial number.
func TestParseUTCOffset(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"+0200", 7200, true},
		{"-0330", -12600, true},
		{"+013000", 5400, true},
		{"+0000", 0, true},
		{"0200", 0, false},
		{"+2400", 0, false},
		{"+0060", 0, false},
		{"+02", 0, false},
		{"+ 200", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parseUTCOffset(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseUTCOffset(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
