package main

import (
	"errors"
	"testing"

	"hermex/internal/directory"
)

// TestApplyActiveSyncSizeLimit proves the request-body cap is applied only on a clean
// read; a read error or a missing row leaves it untouched, so a transient directory
// failure never shrinks the running cap to zero.
func TestApplyActiveSyncSizeLimit(t *testing.T) {
	const sentinel int64 = -1

	got := sentinel
	applyActiveSyncSizeLimit(
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{ActiveSyncRequestBytes: 16384}, true, nil
		},
		func(n int64) { got = n })
	if got != 16384 {
		t.Errorf("applied cap = %d, want 16384", got)
	}

	got = sentinel
	applyActiveSyncSizeLimit(
		func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, errors.New("db down") },
		func(n int64) { got = n })
	if got != sentinel {
		t.Errorf("setter called on read error (got %d); the cap must be left unchanged", got)
	}

	got = sentinel
	applyActiveSyncSizeLimit(
		func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil },
		func(n int64) { got = n })
	if got != sentinel {
		t.Errorf("setter called with no stored row (got %d); the default must stand", got)
	}
}
