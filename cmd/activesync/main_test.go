package main

import (
	"errors"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
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
	// found is true beside the error, so only the error guard stands between a
	// half-read row and the setters: the !found guard cannot hide its loss.
	got = applyWith(func() (directory.SizeLimits, bool, error) {
		return stored, true, errors.New("db down")
	})
	if got != untouched {
		t.Errorf("a read error applied %+v; both caps must be left unchanged", got)
	}

	got = applyWith(func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil })
	if got != untouched {
		t.Errorf("a missing row applied %+v; the defaults must stand", got)
	}
}

// captureSink collects the events an applier emits.
type captureSink struct{ events []logging.Event }

func (s *captureSink) Write(e logging.Event) { s.events = append(s.events, e) }

// TestApplyActiveSyncSizeLimitRecordsAReadFailure proves the read failure the
// applier swallows to keep the running caps is still recorded in the central log,
// the operator's only view of a settings row that stopped being read.
func TestApplyActiveSyncSizeLimitRecordsAReadFailure(t *testing.T) {
	sink := &captureSink{}
	applyActiveSyncSizeLimit(logging.New(sink),
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{}, false, errors.New("db down")
		},
		func(int64) {}, func(int64) {})
	if len(sink.events) != 1 || sink.events[0].Name != "settings.read.fail" || sink.events[0].Fields["settings"] != "size-limits" {
		t.Errorf("events = %+v, want one settings.read.fail for size-limits", sink.events)
	}
}
