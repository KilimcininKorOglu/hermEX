package oxcical

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func mustRRule(t *testing.T, v string) Recurrence {
	t.Helper()
	r, ok := parseRRule(v)
	if !ok {
		t.Fatalf("parseRRule(%q) failed", v)
	}
	return r
}

// TestOccurrencesWeekly checks a plain weekly rule steps by 7 days and keeps the
// series start's clock time, bounded to the window.
func TestOccurrencesWeekly(t *testing.T) {
	start := time.Date(2026, 1, 5, 14, 0, 0, 0, time.UTC) // Monday
	occ := mustRRule(t, "FREQ=WEEKLY").Occurrences(start, start, start.AddDate(0, 0, 28), 0)
	if len(occ) != 4 {
		t.Fatalf("weekly: got %d occurrences, want 4: %v", len(occ), occ)
	}
	for i, o := range occ {
		if want := start.AddDate(0, 0, 7*i); !o.Equal(want) {
			t.Errorf("occurrence %d = %v, want %v", i, o, want)
		}
	}
}

// TestOccurrencesWeeklyByDay checks a weekly BYDAY rule emits each listed weekday.
func TestOccurrencesWeeklyByDay(t *testing.T) {
	start := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC) // Monday
	occ := mustRRule(t, "FREQ=WEEKLY;BYDAY=MO,WE,FR").Occurrences(start, start, start.AddDate(0, 0, 14), 0)
	if len(occ) != 6 {
		t.Fatalf("weekly byday: got %d occurrences, want 6: %v", len(occ), occ)
	}
	for _, o := range occ {
		switch o.Weekday() {
		case time.Monday, time.Wednesday, time.Friday:
		default:
			t.Errorf("occurrence on %v, want Mon/Wed/Fri", o.Weekday())
		}
		if o.Hour() != 9 {
			t.Errorf("occurrence %v lost the series clock time", o)
		}
	}
}

// TestOccurrencesCount confirms COUNT bounds the whole series, not the window.
func TestOccurrencesCount(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	occ := mustRRule(t, "FREQ=DAILY;COUNT=3").Occurrences(start, start, start.AddDate(1, 0, 0), 0)
	if len(occ) != 3 {
		t.Fatalf("count: got %d occurrences, want 3: %v", len(occ), occ)
	}
}

// TestOccurrencesUntil confirms UNTIL is inclusive.
func TestOccurrencesUntil(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	occ := mustRRule(t, "FREQ=DAILY;UNTIL=20260105T120000Z").Occurrences(start, start, start.AddDate(1, 0, 0), 0)
	if len(occ) != 5 {
		t.Fatalf("until: got %d occurrences, want 5: %v", len(occ), occ)
	}
}

// TestOccurrencesMonthly checks a monthly rule lands on the same day each month.
func TestOccurrencesMonthly(t *testing.T) {
	start := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	occ := mustRRule(t, "FREQ=MONTHLY").Occurrences(start, start, start.AddDate(0, 3, 0), 0)
	if len(occ) != 3 {
		t.Fatalf("monthly: got %d occurrences, want 3: %v", len(occ), occ)
	}
}

// wantDates checks occ lands on exactly the given dates, in order.
func wantDates(t *testing.T, occ []time.Time, want ...string) {
	t.Helper()
	got := make([]string, len(occ))
	for i, o := range occ {
		got[i] = o.Format("2006-01-02")
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("occurrences = %v, want %v", got, want)
	}
}

// TestOccurrencesDayPins expands the monthly and yearly day pins. Before, the pins were
// ignored and every such rule stepped on the series start's day of month, so "the
// second Tuesday" landed on the same date each month instead of on a Tuesday.
func TestOccurrencesDayPins(t *testing.T) {
	cases := []struct {
		name, rule string
		start      time.Time
		want       []string
	}{
		{"second Tuesday by ordinal BYDAY", "FREQ=MONTHLY;BYDAY=2TU;COUNT=3",
			time.Date(2026, 1, 13, 9, 0, 0, 0, time.UTC), []string{"2026-01-13", "2026-02-10", "2026-03-10"}},
		{"second Tuesday by BYSETPOS", "FREQ=MONTHLY;BYDAY=TU;BYSETPOS=2;COUNT=3",
			time.Date(2026, 1, 13, 9, 0, 0, 0, time.UTC), []string{"2026-01-13", "2026-02-10", "2026-03-10"}},
		{"last weekday", "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1;COUNT=3",
			time.Date(2026, 1, 30, 9, 0, 0, 0, time.UTC), []string{"2026-01-30", "2026-02-27", "2026-03-31"}},
		{"31st skips short months", "FREQ=MONTHLY;BYMONTHDAY=31;COUNT=3",
			time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC), []string{"2026-01-31", "2026-03-31", "2026-05-31"}},
		{"last day of month", "FREQ=MONTHLY;BYMONTHDAY=-1;COUNT=2",
			time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC), []string{"2026-01-31", "2026-02-28"}},
		{"fourth Thursday of November", "FREQ=YEARLY;BYMONTH=11;BYDAY=4TH;COUNT=2",
			time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC), []string{"2026-11-26", "2027-11-25"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			occ := mustRRule(t, c.rule).Occurrences(c.start, c.start, c.start.AddDate(2, 0, 0), 0)
			wantDates(t, occ, c.want...)
		})
	}
}

// TestALongRunningSeriesStillOccursInTodaysWindow opens a window years after the
// series began. RFC 5545 3.3.10 defines every instance from DTSTART on, however
// far back DTSTART is, so a daily stand-up created in 2010 still occurs this week.
// The expansion's work bound must therefore not be spent walking the years before
// the window. With COUNT the skipped instances still count: a series whose COUNT
// runs out before the window has none in it, and one that runs out inside it stops
// there. Instances keep DTSTART's local clock time across every DST change.
func TestALongRunningSeriesStillOccursInTodaysWindow(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2010, 1, 4, 9, 0, 0, 0, berlin)
	ws := time.Date(2026, 1, 5, 0, 0, 0, 0, berlin)
	we := ws.AddDate(0, 0, 7)

	daily := mustRRule(t, "FREQ=DAILY").Occurrences(start, ws, we, 0)
	wantDates(t, daily, "2026-01-05", "2026-01-06", "2026-01-07", "2026-01-08", "2026-01-09", "2026-01-10", "2026-01-11")
	for _, o := range daily {
		if o.Hour() != 9 || o.Location() != berlin {
			t.Errorf("occurrence %v, want 09:00 Europe/Berlin", o)
		}
	}
	summer := mustRRule(t, "FREQ=DAILY").Occurrences(start, time.Date(2026, 7, 1, 0, 0, 0, 0, berlin), time.Date(2026, 7, 2, 0, 0, 0, 0, berlin), 0)
	if len(summer) != 1 || summer[0].Hour() != 9 {
		t.Errorf("summer occurrence = %v, want one at 09:00 local", summer)
	}
	wantDates(t, mustRRule(t, "FREQ=DAILY;INTERVAL=3").Occurrences(start, ws, we, 0), "2026-01-07", "2026-01-10")

	// Instances from 2010-01-04 through 2026-01-07 inclusive, counted on the calendar.
	upTo7th := int(time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC).Sub(time.Date(2010, 1, 4, 0, 0, 0, 0, time.UTC)).Hours()/24) + 1
	wantDates(t, mustRRule(t, fmt.Sprintf("FREQ=DAILY;COUNT=%d", upTo7th)).Occurrences(start, ws, we, 0), "2026-01-05", "2026-01-06", "2026-01-07")
	if got := mustRRule(t, "FREQ=DAILY;COUNT=5000").Occurrences(start, ws, we, 0); len(got) != 0 {
		t.Errorf("a series whose COUNT ended in 2023 occurs %v in 2026, want none", got)
	}
}

// TestOccurrencesWindowOffset confirms pre-window occurrences are skipped from the
// result but the window's own instances are returned.
func TestOccurrencesWindowOffset(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	occ := mustRRule(t, "FREQ=DAILY").Occurrences(start,
		time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC), 0)
	if len(occ) != 3 {
		t.Fatalf("window offset: got %d occurrences, want 3: %v", len(occ), occ)
	}
	if want := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC); !occ[0].Equal(want) {
		t.Errorf("first occurrence %v, want %v", occ[0], want)
	}
}
