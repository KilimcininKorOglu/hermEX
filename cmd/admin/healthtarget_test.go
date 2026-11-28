package main

import (
	"errors"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestAddHealthTargetIsSafeToRerun drives the subcommand an installer runs on
// every install and update. The first run stores the target; a later run with a
// name the table already holds must succeed without a second row, or every
// update would fail; but a target the directory refuses as unusable, or a
// database failure, must stop the installer rather than leave the monitor
// silently incomplete.
func TestAddHealthTargetIsSafeToRerun(t *testing.T) {
	stored := map[string]string{}
	add := func(ht directory.HealthTarget) (int64, error) {
		if prev, ok := stored[ht.Name]; ok {
			t.Logf("rerun keeps the stored %s", prev)
			return 0, directory.ErrHealthTargetExists
		}
		stored[ht.Name] = ht.URL
		return int64(len(stored)), nil
	}

	want := map[int]string{1: `health target "imap" added`, 2: `health target "imap" already present, left unchanged`}
	for run := 1; run <= 2; run++ {
		msg, err := addHealthTarget(add, "imap", "https://127.0.0.1:8091/healthz")
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if msg != want[run] {
			t.Errorf("run %d: message %q, want %q", run, msg, want[run])
		}
	}
	if len(stored) != 1 || stored["imap"] != "https://127.0.0.1:8091/healthz" {
		t.Errorf("stored = %v, want the one imap target", stored)
	}
}

// TestAddHealthTargetReportsARefusal: a target the directory refuses as unusable,
// or a database failure, stops the installer instead of reading as success.
func TestAddHealthTargetReportsARefusal(t *testing.T) {
	for name, failure := range map[string]error{
		"unusable target": directory.ErrInvalidHealthTarget,
		"database down":   errors.New("connection refused"),
	} {
		refuse := func(directory.HealthTarget) (int64, error) { return 0, failure }
		msg, err := addHealthTarget(refuse, "imap", "ftp://x")
		if !errors.Is(err, failure) {
			t.Errorf("%s: err = %v, want it reported", name, err)
		}
		if strings.Contains(msg, "added") || strings.Contains(msg, "present") {
			t.Errorf("%s: message %q claims success", name, msg)
		}
	}
}
