package oxcical

import (
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
