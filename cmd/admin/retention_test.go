package main

import (
	"testing"
	"time"
)

// TestRetentionCutoffNeverReachesTheFuture proves a retention window, however
// wide, puts the prune cutoff in the past: the log prune and prune-eml delete
// everything older than the cutoff, so a cutoff after now would delete every log
// event or cached copy. time.Duration holds about 106751 days, and 150000 days
// times 24 h wraps int64 nanoseconds negative.
func TestRetentionCutoffNeverReachesTheFuture(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for _, days := range []int{1, 30, 106751, 106752, 150000, 1 << 30} {
		if got := retentionCutoff(now, days); !got.Before(now) {
			t.Errorf("retentionCutoff(now, %d) = %v, want a time before %v", days, got, now)
		}
	}
	// The ordinary window is exactly that many calendar days back.
	want := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	if got := retentionCutoff(now, 30); !got.Equal(want) {
		t.Errorf("retentionCutoff(now, 30) = %v, want %v", got, want)
	}
}
