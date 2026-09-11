package main

import (
	"errors"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// TestApplySMTPLineLimit proves the command-line cap is applied only when a stored
// row is read cleanly: a read error or a missing row leaves the cap untouched. The
// guard is load-bearing, without it a transient directory failure would push a
// zero-valued SizeLimits to the server and shrink the cap to nothing.
func TestApplySMTPLineLimit(t *testing.T) {
	const sentinel int64 = -1

	// A clean read applies the stored cap verbatim.
	got := sentinel
	applySMTPLineLimit(nil,
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{SMTPCommandLineBytes: 1024}, true, nil
		},
		func(n int64) { got = n })
	if got != 1024 {
		t.Errorf("applied cap = %d, want 1024", got)
	}

	// A read error must NOT call the setter, the running cap stays as it is.
	got = sentinel
	applySMTPLineLimit(nil,
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{}, false, errors.New("db down")
		},
		func(n int64) { got = n })
	if got != sentinel {
		t.Errorf("setter called on read error (got %d); the cap must be left unchanged", got)
	}

	// No stored row must NOT call the setter, the built-in default stands.
	got = sentinel
	applySMTPLineLimit(nil,
		func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil },
		func(n int64) { got = n })
	if got != sentinel {
		t.Errorf("setter called with no stored row (got %d); the default must stand", got)
	}
}

// TestApplySMTPLineLimitRecordsAReadFailure proves this daemon-local applier records
// its swallowed read failure in the central log too. Each daemon carries its own copy
// of this shape, so the class is only covered if a local copy is proven as well.
func TestApplySMTPLineLimitRecordsAReadFailure(t *testing.T) {
	sink := &sweepSink{}
	applySMTPLineLimit(logging.New(sink),
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{}, false, errors.New("db down")
		},
		func(int64) {})

	e, ok := sink.find("settings.read.fail")
	if !ok {
		t.Fatal("read failure emitted no settings.read.fail event")
	}
	if e.Fields["settings"] != "size-limits" {
		t.Errorf("event fields = %v, want settings=size-limits", e.Fields)
	}

	// Negative control: a clean read stays silent, the poll runs every minute.
	quiet := &sweepSink{}
	applySMTPLineLimit(logging.New(quiet),
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{SMTPCommandLineBytes: 1024}, true, nil
		},
		func(int64) {})
	if _, ok := quiet.find("settings.read.fail"); ok {
		t.Error("a clean read emitted settings.read.fail, want silence")
	}
}
