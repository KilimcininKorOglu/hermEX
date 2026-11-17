package main

import (
	"errors"
	"testing"

	"hermex/internal/directory"
)

// recordedLimits captures what applyActiveSyncSizeLimit hands to its two setters.
type recordedLimits struct {
	requestBody, freeBusyTargets int64
}

// applyWith runs applyActiveSyncSizeLimit over one read result and returns what
// each setter received; a setter that was not called keeps -1.
func applyWith(read func() (directory.SizeLimits, bool, error)) recordedLimits {
	got := recordedLimits{requestBody: -1, freeBusyTargets: -1}
	applyActiveSyncSizeLimit(nil, read,
		func(n int64) { got.requestBody = n },
		func(n int64) { got.freeBusyTargets = n })
	return got
}

// TestApplyActiveSyncSizeLimit proves the two operator-set limits the size-limits
// row carries (the request-body cap and the free/busy target cap) both reach the
// running daemon on a clean read, and that a read error or a missing row leaves
// both untouched, so a transient directory failure never shrinks a running cap
// to zero.
func TestApplyActiveSyncSizeLimit(t *testing.T) {
	stored := directory.SizeLimits{ActiveSyncRequestBytes: 16384, FreeBusyMaxTargets: 7}
	got := applyWith(func() (directory.SizeLimits, bool, error) { return stored, true, nil })
	if got != (recordedLimits{requestBody: 16384, freeBusyTargets: 7}) {
		t.Errorf("applied = %+v, want the stored request cap 16384 and free/busy cap 7", got)
	}

	untouched := recordedLimits{requestBody: -1, freeBusyTargets: -1}
	got = applyWith(func() (directory.SizeLimits, bool, error) {
		return stored, false, errors.New("db down")
	})
	if got != untouched {
		t.Errorf("a read error applied %+v; both caps must be left unchanged", got)
	}

	got = applyWith(func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil })
	if got != untouched {
		t.Errorf("a missing row applied %+v; the defaults must stand", got)
	}
}
