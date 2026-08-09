package main

import (
	"errors"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// TestApplyIMAPSizeLimit proves the literal cap is applied only when a stored row is
// read cleanly: a read error or a missing row leaves the cap untouched. The guard is
// load-bearing, without it a transient directory failure would push a zero-valued
// SizeLimits to the server and silently shrink the IMAP literal cap to nothing.
func TestApplyIMAPSizeLimit(t *testing.T) {
	const sentinel int64 = -1

	// A clean read applies the stored cap verbatim.
	got := sentinel
	applyIMAPSizeLimit(nil,
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{IMAPLiteralBytes: 4096}, true, nil
		},
		func(n int64) { got = n })
	if got != 4096 {
		t.Errorf("applied cap = %d, want 4096", got)
	}

	// A read error must NOT call the setter, the running cap stays as it is.
	got = sentinel
	applyIMAPSizeLimit(nil,
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{}, false, errors.New("db down")
		},
		func(n int64) { got = n })
	if got != sentinel {
		t.Errorf("setter called on read error (got %d); the cap must be left unchanged", got)
	}

	// No stored row must NOT call the setter, the built-in default stands.
	got = sentinel
	applyIMAPSizeLimit(nil,
		func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil },
		func(n int64) { got = n })
	if got != sentinel {
		t.Errorf("setter called with no stored row (got %d); the default must stand", got)
	}
}

// captureSink collects the events an applier emits.
type captureSink struct{ events []logging.Event }

func (s *captureSink) Write(e logging.Event) { s.events = append(s.events, e) }

// TestApplyIMAPSizeLimitRecordsAReadFailure proves a daemon-local applier records its
// swallowed read failure in the central log too, not just the shared authlimit and
// httplimit helpers. Each daemon carries its own copy of this shape, so the class is
// only covered if a local copy is proven as well.
func TestApplyIMAPSizeLimitRecordsAReadFailure(t *testing.T) {
	sink := &captureSink{}
	applyIMAPSizeLimit(logging.New(sink),
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{}, false, errors.New("db down")
		},
		func(int64) {})

	if len(sink.events) != 1 {
		t.Fatalf("read failure emitted %d events, want 1", len(sink.events))
	}
	if e := sink.events[0]; e.Name != "settings.read.fail" || e.Fields["settings"] != "size-limits" {
		t.Errorf("event = %s %v, want settings.read.fail for size-limits", e.Name, e.Fields)
	}

	// Negative control: a clean read stays silent, the poll runs every minute.
	quiet := &captureSink{}
	applyIMAPSizeLimit(logging.New(quiet),
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{IMAPLiteralBytes: 4096}, true, nil
		},
		func(int64) {})
	if len(quiet.events) != 0 {
		t.Errorf("a clean read emitted %d events, want none", len(quiet.events))
	}
}
