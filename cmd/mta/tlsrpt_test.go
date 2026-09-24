package main

import (
	"errors"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// TestApplyTLSReportLimit proves the TLS report body cap is applied only from a
// cleanly read row, and that a failed read is recorded. Without the guard a
// transient directory failure would push a zero cap to the handler.
func TestApplyTLSReportLimit(t *testing.T) {
	const sentinel int64 = -1
	got := sentinel
	applyTLSReportLimit(nil,
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{TLSReportBytes: 4096}, true, nil
		},
		func(n int64) { got = n })
	if got != 4096 {
		t.Errorf("applied cap = %d, want 4096", got)
	}

	got = sentinel
	sink := &sweepSink{}
	applyTLSReportLimit(logging.New(sink),
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{}, false, errors.New("db down")
		},
		func(n int64) { got = n })
	if got != sentinel {
		t.Errorf("setter called on read error (got %d); the cap must be left unchanged", got)
	}
	if _, ok := sink.find("settings.read.fail"); !ok {
		t.Error("read failure emitted no settings.read.fail event")
	}

	applyTLSReportLimit(nil,
		func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil },
		func(n int64) { got = n })
	if got != sentinel {
		t.Errorf("setter called with no stored row (got %d); the default must stand", got)
	}
}
