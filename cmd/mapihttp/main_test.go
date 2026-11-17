package main

import (
	"errors"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// TestApplyMapiSizeLimit proves the MAPI/HTTP request-body cap is applied only on a
// clean read; a read error or a missing row leaves it untouched, so a transient
// directory failure never shrinks the running cap to zero.
func TestApplyMapiSizeLimit(t *testing.T) {
	const sentinel int64 = -1

	got := sentinel
	applyMapiSizeLimit(nil,
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{MapiRequestBytes: 8192}, true, nil
		},
		func(n int64) { got = n })
	if got != 8192 {
		t.Errorf("applied cap = %d, want 8192", got)
	}

	got = sentinel
	applyMapiSizeLimit(nil,
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{}, false, errors.New("db down")
		},
		func(n int64) { got = n })
	if got != sentinel {
		t.Errorf("setter called on read error (got %d); the cap must be left unchanged", got)
	}

	got = sentinel
	applyMapiSizeLimit(nil,
		func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil },
		func(n int64) { got = n })
	if got != sentinel {
		t.Errorf("setter called with no stored row (got %d); the default must stand", got)
	}
}

// captureSink collects the events an applier emits.
type captureSink struct{ events []logging.Event }

func (s *captureSink) Write(e logging.Event) { s.events = append(s.events, e) }

// TestApplyMapiSizeLimitRecordsAReadFailure proves the read failure the applier
// swallows to keep the running cap is still recorded in the central log, the
// operator's only view of a settings row that stopped being read.
func TestApplyMapiSizeLimitRecordsAReadFailure(t *testing.T) {
	sink := &captureSink{}
	applyMapiSizeLimit(logging.New(sink),
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{}, false, errors.New("db down")
		},
		func(int64) {})
	if len(sink.events) != 1 || sink.events[0].Name != "settings.read.fail" || sink.events[0].Fields["settings"] != "size-limits" {
		t.Errorf("events = %+v, want one settings.read.fail for size-limits", sink.events)
	}
}
