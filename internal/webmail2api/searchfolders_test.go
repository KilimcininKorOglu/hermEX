package webmail2api

import (
	"testing"
	"time"
)

// TestSearchDatesAreTheCallersDays checks that a saved search's day range is
// read as whole days in the caller's zone, so mail the caller sees on the to
// day is inside and mail it sees on the next day is outside.
func TestSearchDatesAreTheCallersDays(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	sf := searchFolderJSON{DateFrom: "2026-11-26", DateTo: "2026-11-26"}
	cases := []struct {
		at   string
		want bool
	}{
		{"2026-11-26T04:59:00Z", false}, // 25 Nov 23:59 in New York
		{"2026-11-26T05:00:00Z", true},  // 26 Nov 00:00
		{"2026-11-27T02:00:00Z", true},  // 26 Nov 21:00, already 27 Nov in UTC
		{"2026-11-27T05:00:00Z", false}, // 27 Nov 00:00
	}
	for _, c := range cases {
		at, err := time.Parse(time.RFC3339, c.at)
		if err != nil {
			t.Fatal(err)
		}
		if got := inSearchDates(sf, at, ny); got != c.want {
			t.Errorf("inSearchDates(%s) = %v, want %v", c.at, got, c.want)
		}
	}
	if !inSearchDates(searchFolderJSON{}, time.Now(), time.UTC) {
		t.Error("a search without dates must match every message")
	}
}
