package ews

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	// Embed the IANA time-zone database into the test binary: the dev container
	// may carry no system zoneinfo, so LoadLocation must not depend on the host.
	_ "time/tzdata"
)

// windowsToIANA maps each served Windows zone id to the IANA zone the test uses
// as the authoritative offset/DST source.
var windowsToIANA = map[string]string{
	"UTC":                     "UTC",
	"GMT Standard Time":       "Europe/London",
	"W. Europe Standard Time": "Europe/Berlin",
	"Turkey Standard Time":    "Europe/Istanbul",
	"Eastern Standard Time":   "America/New_York",
	"Pacific Standard Time":   "America/Los_Angeles",
}

// daysInMonth returns the number of days in (year, month).
func daysInMonth(year int, m time.Month) int {
	return time.Date(year, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// nthSunday returns the day-of-month of the occurrence-th Sunday of (year, month);
// occurrence 5 means the last Sunday, even in a four-Sunday month.
func nthSunday(year int, m time.Month, occurrence int) int {
	first := time.Date(year, m, 1, 0, 0, 0, 0, time.UTC)
	lead := (int(time.Sunday) - int(first.Weekday()) + 7) % 7
	day := 1 + lead + (occurrence-1)*7
	for day > daysInMonth(year, m) {
		day -= 7
	}
	return day
}

// offsetChangeDay returns the day in (year, month) on which the location's UTC
// offset changes, sampling at noon to stay clear of the ambiguous transition
// hour, or 0 if the offset is constant across the month.
func offsetChangeDay(loc *time.Location, year int, m time.Month) int {
	prev := 0
	have := false
	for d := 1; d <= daysInMonth(year, m); d++ {
		_, off := time.Date(year, m, d, 12, 0, 0, 0, loc).Zone()
		if have && off != prev {
			return d
		}
		prev, have = off, true
	}
	return 0
}

// TestServerTimeZonesMatchTZData is the table's authoritative check: every served
// zone's standard and daylight biases, and every DST transition day, must agree
// with Go's embedded IANA database. This converts the hand-curated rules from
// "recalled and plausible" to "verified against tzdata" — a wrong sign, magnitude,
// month, or occurrence fails here.
func TestServerTimeZonesMatchTZData(t *testing.T) {
	const year = 2024 // a stable year for all served rules (Turkey fixed since 2016).
	for _, z := range serverTimeZones {
		iana, ok := windowsToIANA[z.id]
		if !ok {
			t.Fatalf("no IANA mapping for served zone %q", z.id)
		}
		loc, err := time.LoadLocation(iana)
		if err != nil {
			t.Fatalf("LoadLocation(%q): %v", iana, err)
		}
		// Every served zone is northern hemisphere: January is standard time,
		// July is daylight time. Zone() returns seconds east of UTC; the Windows
		// bias is minutes with the opposite sign (west of UTC positive).
		_, janOff := time.Date(year, 1, 15, 12, 0, 0, 0, loc).Zone()
		_, julOff := time.Date(year, 7, 15, 12, 0, 0, 0, loc).Zone()
		if want := -janOff / 60; z.stdBias != want {
			t.Errorf("%s stdBias = %d, tzdata says %d", z.id, z.stdBias, want)
		}
		if z.dst == nil {
			if janOff != julOff {
				t.Errorf("%s declares no DST but tzdata offset changes (%d → %d)", z.id, janOff, julOff)
			}
			continue
		}
		if want := -julOff / 60; z.dst.dstBias != want {
			t.Errorf("%s dstBias = %d, tzdata says %d", z.id, z.dst.dstBias, want)
		}
		if got, want := offsetChangeDay(loc, year, time.Month(z.dst.startMonth)), nthSunday(year, time.Month(z.dst.startMonth), z.dst.startWeek); got != want {
			t.Errorf("%s DST start: tzdata changes on day %d of month %d, rule predicts %d", z.id, got, z.dst.startMonth, want)
		}
		if got, want := offsetChangeDay(loc, year, time.Month(z.dst.endMonth)), nthSunday(year, time.Month(z.dst.endMonth), z.dst.endWeek); got != want {
			t.Errorf("%s DST end: tzdata changes on day %d of month %d, rule predicts %d", z.id, got, z.dst.endMonth, want)
		}
	}
}

// --- wire tests ---

// getServerTimeZonesReq builds a GetServerTimeZones request, optionally filtered
// to the given zone ids (no ids requests every zone).
func getServerTimeZonesReq(ids ...string) string {
	inner := ""
	if len(ids) > 0 {
		var b strings.Builder
		b.WriteString(`<Ids xmlns:t="` + nsTypes + `">`)
		for _, id := range ids {
			b.WriteString(`<t:Id>`)
			b.WriteString(id)
			b.WriteString(`</t:Id>`)
		}
		b.WriteString(`</Ids>`)
		inner = b.String()
	}
	return wrapRequest(`<GetServerTimeZones xmlns="` + nsMessages + `">` + inner + `</GetServerTimeZones>`)
}

// tzDefTest is a namespace-agnostic view of a returned TimeZoneDefinition (Go xml
// matches by local name down the path), used to assert the response structure.
type tzDefTest struct {
	ID      string `xml:"Id,attr"`
	Periods []struct {
		Bias string `xml:"Bias,attr"`
		Name string `xml:"Name,attr"`
	} `xml:"Periods>Period"`
	Groups []struct {
		ID          string `xml:"Id,attr"`
		Recurrences []struct {
			To struct {
				Kind  string `xml:"Kind,attr"`
				Value string `xml:",chardata"`
			} `xml:"To"`
			Month      int `xml:"Month"`
			Occurrence int `xml:"Occurrence"`
		} `xml:"RecurringDayTransition"`
	} `xml:"TransitionsGroups>TransitionsGroup"`
	Transitions []struct {
		To struct {
			Kind  string `xml:"Kind,attr"`
			Value string `xml:",chardata"`
		} `xml:"To"`
	} `xml:"Transitions>Transition"`
}

// parseTZDefs extracts the TimeZoneDefinition list from a GetServerTimeZones
// response body.
func parseTZDefs(t *testing.T, out string) []tzDefTest {
	t.Helper()
	var env struct {
		Defs []tzDefTest `xml:"Body>GetServerTimeZonesResponse>ResponseMessages>GetServerTimeZonesResponseMessage>TimeZoneDefinitions>TimeZoneDefinition"`
	}
	if err := xml.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal response: %v\n%s", err, out)
	}
	return env.Defs
}

// TestGetServerTimeZonesAll confirms an unfiltered request returns every served
// zone.
func TestGetServerTimeZonesAll(t *testing.T) {
	ts, _ := seededEWS(t)
	resp, out := soapPost(t, ts, getServerTimeZonesReq(), true)
	if resp.StatusCode != 200 || !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("GetServerTimeZones not success (%d): %s", resp.StatusCode, out)
	}
	defs := parseTZDefs(t, out)
	if len(defs) != len(serverTimeZones) {
		t.Fatalf("returned %d zones, want %d", len(defs), len(serverTimeZones))
	}
	got := map[string]bool{}
	for _, d := range defs {
		got[d.ID] = true
	}
	for _, z := range serverTimeZones {
		if !got[z.id] {
			t.Errorf("served zone %q absent from unfiltered response", z.id)
		}
	}
}

// TestGetServerTimeZonesFilterUnknown confirms an Ids filter returns exactly the
// known requested zones — an unknown id is silently absent, not an error.
func TestGetServerTimeZonesFilterUnknown(t *testing.T) {
	ts, _ := seededEWS(t)
	_, out := soapPost(t, ts, getServerTimeZonesReq("UTC", "No Such Zone"), true)
	defs := parseTZDefs(t, out)
	if len(defs) != 1 {
		t.Fatalf("filter [UTC, bogus] returned %d zones, want 1: %s", len(defs), out)
	}
	if defs[0].ID != "UTC" {
		t.Errorf("filtered zone = %q, want UTC", defs[0].ID)
	}
}

// TestGetServerTimeZonesDSTStructure confirms a DST zone is two Periods plus a
// TransitionsGroup of recurring transitions, with a top-level Transition pointing
// at the group.
func TestGetServerTimeZonesDSTStructure(t *testing.T) {
	ts, _ := seededEWS(t)
	_, out := soapPost(t, ts, getServerTimeZonesReq("Pacific Standard Time"), true)
	defs := parseTZDefs(t, out)
	if len(defs) != 1 {
		t.Fatalf("want exactly Pacific Standard Time, got %d defs: %s", len(defs), out)
	}
	d := defs[0]
	if len(d.Periods) != 2 {
		t.Fatalf("DST zone must have 2 periods, got %d", len(d.Periods))
	}
	if d.Periods[0].Bias != "PT8H" || d.Periods[0].Name != "Standard" {
		t.Errorf("standard period = {%s, %s}, want {PT8H, Standard}", d.Periods[0].Bias, d.Periods[0].Name)
	}
	if d.Periods[1].Bias != "PT7H" || d.Periods[1].Name != "Daylight" {
		t.Errorf("daylight period = {%s, %s}, want {PT7H, Daylight}", d.Periods[1].Bias, d.Periods[1].Name)
	}
	if len(d.Groups) != 1 || len(d.Groups[0].Recurrences) != 2 {
		t.Fatalf("DST zone must have one group of two transitions, got %d groups", len(d.Groups))
	}
	start, end := d.Groups[0].Recurrences[0], d.Groups[0].Recurrences[1]
	if start.Month != 3 || start.Occurrence != 2 || start.To.Kind != "Period" {
		t.Errorf("US DST start = month %d occ %d kind %s, want 3/2/Period", start.Month, start.Occurrence, start.To.Kind)
	}
	if end.Month != 11 || end.Occurrence != 1 || end.To.Kind != "Period" {
		t.Errorf("US DST end = month %d occ %d kind %s, want 11/1/Period", end.Month, end.Occurrence, end.To.Kind)
	}
	if len(d.Transitions) != 1 || d.Transitions[0].To.Kind != "Group" {
		t.Errorf("DST zone top-level transition must target a Group, got %+v", d.Transitions)
	}
}

// TestGetServerTimeZonesFixedStructure confirms a fixed-offset zone is a single
// Period with no TransitionsGroups, addressed by a top-level Transition pointing
// directly at the period.
func TestGetServerTimeZonesFixedStructure(t *testing.T) {
	ts, _ := seededEWS(t)
	_, out := soapPost(t, ts, getServerTimeZonesReq("UTC"), true)
	defs := parseTZDefs(t, out)
	if len(defs) != 1 {
		t.Fatalf("want exactly UTC, got %d defs: %s", len(defs), out)
	}
	d := defs[0]
	if len(d.Periods) != 1 || d.Periods[0].Bias != "PT0S" {
		t.Fatalf("UTC must be one PT0S period, got %+v", d.Periods)
	}
	if len(d.Groups) != 0 {
		t.Errorf("fixed-offset zone must have no transitions groups, got %d", len(d.Groups))
	}
	if len(d.Transitions) != 1 || d.Transitions[0].To.Kind != "Period" {
		t.Errorf("fixed-offset zone top-level transition must target a Period, got %+v", d.Transitions)
	}
}
