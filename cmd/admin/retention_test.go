package main

import (
	"bytes"
	"database/sql"
	"log"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// TestSeedLogRetentionReportsAFailedRead proves a directory that cannot be read
// leaves a line naming the failure, instead of a silent skip that leaves the
// operator thinking the config window was seeded, and that no write is tried.
func TestSeedLogRetentionReportsAFailedRead(t *testing.T) {
	db, err := sql.Open("mysql", "u:p@tcp(127.0.0.1:1)/none")
	mustNoErrMain(t, "open a handle", err)
	mustNoErrMain(t, "close the handle so every read fails", db.Close())
	var out bytes.Buffer
	prev, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&out)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(prevFlags) })

	seedLogRetention(directory.NewSQL(db), 30)
	got := out.String()
	if !strings.Contains(got, "read log retention before seeding") || !strings.Contains(got, "closed") {
		t.Errorf("output = %q, want the failed read and its error", got)
	}
	if strings.Contains(got, "seed log retention:") {
		t.Errorf("output = %q: a write was tried after the failed read", got)
	}
}

func mustNoErrMain(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

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
