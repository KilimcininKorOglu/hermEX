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

// TestImportReadsAFloatingTimeInTheDefaultZone is the defect for a value that
// carries no zone at all: RFC 5545 defines it as the reader's own wall clock, so
// reading it as UTC stores it wrong by the mailbox owner's offset. Istanbul is
// UTC+3, so 10:30 there is 07:30Z.
func TestImportReadsAFloatingTimeInTheDefaultZone(t *testing.T) {
	r := newResolver()
	opt := r.opt()
	opt.DefaultZone = mustZone(t, "Europe/Istanbul")
	got := importedStart(t, r, zoneCal("", ""), opt)
	want := time.Date(2026, 8, 11, 7, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("start = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestDefaultZoneSilencesAFloatingTime pairs with the test above: a floating time
// read in the reader's own zone is no longer a loss, so it must stop being
// reported or the log fills with events that name nothing wrong.
func TestDefaultZoneSilencesAFloatingTime(t *testing.T) {
	var notes []ZoneNote
	_, opt := zoneOpt(&notes)
	opt.DefaultZone = mustZone(t, "Europe/Istanbul")
	if _, err := Import(zoneCal("", ""), opt); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("reported %d floating times that were read in the reader's zone: %+v", len(notes), notes)
	}
}

// TestDefaultZoneDoesNotCoverAFailedTZID draws the line: the sender named a zone
// and this package did not understand it. Putting the reader's zone on that value
// would replace one wrong instant with another that is harder to spot, so the
// value stays read as UTC and reported.
func TestDefaultZoneDoesNotCoverAFailedTZID(t *testing.T) {
	var notes []ZoneNote
	r, opt := zoneOpt(&notes)
	opt.DefaultZone = mustZone(t, "Europe/Istanbul")
	got := importedStart(t, r, zoneCal(";TZID=Mars/Olympus", ""), opt)
	if !got.Equal(time.Date(2026, 8, 11, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("start = %s, want the value read as UTC", got.Format(time.RFC3339))
	}
	if len(notes) == 0 {
		t.Fatal("a TZID nothing resolved was silently given the reader's zone")
	}
}

// mustZone loads an IANA zone or fails the test.
func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
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

// shiftingVTZ is a zone no table knows, described only by its own transition
// rules: UTC+1 in winter, UTC+2 from the last Sunday in March to the last Sunday
// in October. It is the shape a client that is neither Outlook nor an IANA-naming
// client writes.
const shiftingVTZ = "BEGIN:VTIMEZONE\r\nTZID:Acme/Shifting\r\n" +
	"BEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nRRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\n" +
	"TZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\n" +
	"TZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n"

// TestImportEvaluatesVTimezoneTransitions covers a zone that no table names and
// that changes offset during the year: the rules in the stream are evaluated for
// the value's own date, so a summer time takes the summer offset. One fixed
// offset would be wrong for half the year, which is why the rules are read rather
// than an offset picked.
func TestImportEvaluatesVTimezoneTransitions(t *testing.T) {
	r := newResolver()
	got := importedStart(t, r, zoneCal(";TZID=Acme/Shifting", shiftingVTZ), r.opt())
	want := time.Date(2026, 8, 11, 8, 30, 0, 0, time.UTC) // 10:30 at +0200
	if !got.Equal(want) {
		t.Fatalf("start = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestImportEvaluatesVTimezoneWinterTransition is the other side of the same
// rules: a January time takes the winter offset, so the transition is really
// being evaluated rather than one offset being applied to everything.
func TestImportEvaluatesVTimezoneWinterTransition(t *testing.T) {
	cal := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + shiftingVTZ +
		"BEGIN:VEVENT\r\nUID:zone-2\r\nSUMMARY:Winter\r\n" +
		"DTSTART;TZID=Acme/Shifting:20260114T103000\r\nDTEND;TZID=Acme/Shifting:20260114T113000\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n")
	r := newResolver()
	got := importedStart(t, r, cal, r.opt())
	want := time.Date(2026, 1, 14, 9, 30, 0, 0, time.UTC) // 10:30 at +0100
	if !got.Equal(want) {
		t.Fatalf("start = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestImportReportsAnUnusableVTimezone keeps the refusal honest: a VTIMEZONE whose
// rules carry no offset describes nothing, so its times stay unresolved and are
// reported instead of being read at a guessed offset.
func TestImportReportsAnUnusableVTimezone(t *testing.T) {
	vtz := "BEGIN:VTIMEZONE\r\nTZID:Acme/Empty\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n"
	var notes []ZoneNote
	r, opt := zoneOpt(&notes)
	got := importedStart(t, r, zoneCal(";TZID=Acme/Empty", vtz), opt)
	if !got.Equal(time.Date(2026, 8, 11, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("start = %s, want the value read as UTC", got.Format(time.RFC3339))
	}
	if len(notes) == 0 {
		t.Fatal("a VTIMEZONE that could not be resolved was not reported")
	}
}

// TestNthWeekdayOf covers the ordinal-weekday arithmetic every transition rule
// rests on, including the month whose ordinal does not exist.
func TestNthWeekdayOf(t *testing.T) {
	cases := []struct {
		month time.Month
		wd    time.Weekday
		nth   int
		day   int
		ok    bool
	}{
		{time.March, time.Sunday, -1, 29, true},      // last Sunday in March 2026
		{time.October, time.Sunday, -1, 25, true},    // last Sunday in October 2026
		{time.March, time.Sunday, 1, 1, true},        // first Sunday in March 2026
		{time.March, time.Sunday, 2, 8, true},        // second Sunday
		{time.February, time.Sunday, 5, 0, false},    // February 2026 has four Sundays
		{time.November, time.Sunday, 5, 29, true},    // November 2026 has five
		{time.December, time.Thursday, -1, 31, true}, // the year-end wrap
	}
	for _, c := range cases {
		got, ok := nthWeekdayOf(2026, c.month, c.wd, c.nth, 2, 0, 0)
		if ok != c.ok || (ok && got.Day() != c.day) {
			t.Errorf("nthWeekdayOf(2026, %v, %v, %d) = %v, %v; want day %d, %v",
				c.month, c.wd, c.nth, got.Format("2006-01-02"), ok, c.day, c.ok)
		}
	}
}

// TestWindowsZonesLoad requires every mapped IANA name to load, so a typo in the
// table fails here instead of shifting an appointment by an hour in production,
// and requires every key to resolve whatever case a TZID arrives in.
func TestWindowsZonesLoad(t *testing.T) {
	for win, iana := range windowsZones {
		if _, err := time.LoadLocation(iana); err != nil {
			t.Errorf("%s -> %s: %v", win, iana, err)
		}
		for _, spelling := range []string{win, strings.ToLower(win), strings.ToUpper(win)} {
			if got, ok := ianaForWindows(spelling); !ok || got != iana {
				t.Errorf("ianaForWindows(%q) = %q, %v; want %q", spelling, got, ok, iana)
			}
		}
	}
}

// TestWindowsZoneFor covers the reverse lookup the time zone definition's
// KeyName comes from: the canonical Windows spelling, one fixed answer for an
// IANA zone two Windows ids share, and a refusal for a zone the table lacks.
func TestWindowsZoneFor(t *testing.T) {
	cases := []struct {
		iana string
		want string
		ok   bool
	}{
		{"Europe/Berlin", "W. Europe Standard Time", true},
		{"Europe/Istanbul", "Turkey Standard Time", true},
		{"Asia/Novosibirsk", "N. Central Asia Standard Time", true},
		{"Europe/Amsterdam", "", false},
	}
	for _, c := range cases {
		got, ok := WindowsZoneFor(c.iana)
		if got != c.want || ok != c.ok {
			t.Errorf("WindowsZoneFor(%q) = %q, %v; want %q, %v", c.iana, got, ok, c.want, c.ok)
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
