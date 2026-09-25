package oxcical

import (
	"strings"
	"testing"
	"time"
)

// TestVTimezoneDescribesTheZone reads a generated VTIMEZONE back through the
// same path an imported calendar's own VTIMEZONE takes, and requires it to give
// the zone's real offset for every wall clock over two years. That is the only
// thing a client without the IANA name can place a time by. Wall clocks within
// two hours of a switch are skipped, because a wall clock there is ambiguous or
// does not exist.
func TestVTimezoneDescribesTheZone(t *testing.T) {
	for _, name := range []string{"Europe/Berlin", "America/New_York", "Europe/Istanbul", "Australia/Sydney"} {
		loc := mustZone(t, name)
		z := generatedZone(t, loc, 2026)
		for at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); at.Year() < 2028; at = at.Add(6 * time.Hour) {
			if nearSwitch(at, loc) {
				continue
			}
			wall := at.In(loc)
			got, ok := z.offsetAt(time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, time.UTC))
			if want := offsetOf(wall); !ok || got != want {
				t.Fatalf("%s at %s: offset %d, %v; want %d", name, wall.Format(time.RFC3339), got, ok, want)
			}
		}
	}
}

// TestVTimezoneShape pins the block layout: a daylight-saving zone repeats its
// two switches yearly on the ordinal weekday, and a zone without daylight saving
// is one STANDARD block.
func TestVTimezoneShape(t *testing.T) {
	berlin := strings.Join(VTimezone(mustZone(t, "Europe/Berlin"), 2026), "\n")
	for _, want := range []string{
		"TZID:Europe/Berlin",
		"BEGIN:DAYLIGHT\nDTSTART:20260329T020000\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\nTZOFFSETFROM:+0100\nTZOFFSETTO:+0200\nEND:DAYLIGHT",
		"BEGIN:STANDARD\nDTSTART:20261025T030000\nRRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\nTZOFFSETFROM:+0200\nTZOFFSETTO:+0100\nEND:STANDARD",
	} {
		if !strings.Contains(berlin, want) {
			t.Errorf("Berlin VTIMEZONE lacks %q:\n%s", want, berlin)
		}
	}
	newYork := strings.Join(VTimezone(mustZone(t, "America/New_York"), 2026), "\n")
	if !strings.Contains(newYork, "RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU") {
		t.Errorf("New York spring switch is not the second Sunday:\n%s", newYork)
	}
	istanbul := strings.Join(VTimezone(mustZone(t, "Europe/Istanbul"), 2026), "\n")
	want := "BEGIN:VTIMEZONE\nTZID:Europe/Istanbul\nBEGIN:STANDARD\nDTSTART:19700101T000000\nTZOFFSETFROM:+0300\nTZOFFSETTO:+0300\nEND:STANDARD\nEND:VTIMEZONE"
	if istanbul != want {
		t.Errorf("Istanbul VTIMEZONE =\n%s\nwant\n%s", istanbul, want)
	}
}

// TestZoneByIDRefusesUnnamedZones keeps an empty TZID from being read as UTC and
// "Local" from being read as the server's own zone.
func TestZoneByIDRefusesUnnamedZones(t *testing.T) {
	for _, id := range []string{"", "Local", "Mars/Olympus"} {
		if loc := ZoneByID(id); loc != nil {
			t.Errorf("ZoneByID(%q) = %v, want nil", id, loc)
		}
	}
	if loc := ZoneByID("W. Europe Standard Time"); loc == nil || loc.String() != "Europe/Berlin" {
		t.Errorf("ZoneByID(Windows id) = %v, want Europe/Berlin", loc)
	}
}

// generatedZone parses a generated VTIMEZONE into the reader's zone model.
func generatedZone(t *testing.T, loc *time.Location, year int) *vzone {
	t.Helper()
	body := "BEGIN:VCALENDAR\r\n" + strings.Join(VTimezone(loc, year), "\r\n") + "\r\nEND:VCALENDAR\r\n"
	cal, err := parseICal([]byte(body))
	if err != nil {
		t.Fatalf("parse generated VTIMEZONE: %v", err)
	}
	z := collectVZones(cal)[loc.String()]
	if z == nil {
		t.Fatalf("generated VTIMEZONE for %s has no usable rule:\n%s", loc, body)
	}
	return z
}

// nearSwitch reports whether an offset change falls within two hours of at.
func nearSwitch(at time.Time, loc *time.Location) bool {
	off := offsetOf(at.In(loc))
	return offsetOf(at.Add(-2*time.Hour).In(loc)) != off || offsetOf(at.Add(2*time.Hour).In(loc)) != off
}
