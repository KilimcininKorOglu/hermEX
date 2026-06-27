package main

import (
	"errors"
	"testing"

	"hermex/internal/directory"
)

// TestApplyIMAPSizeLimit proves the literal cap is applied only when a stored row is
// read cleanly: a read error or a missing row leaves the cap untouched. The guard is
// load-bearing — without it a transient directory failure would push a zero-valued
// SizeLimits to the server and silently shrink the IMAP literal cap to nothing.
func TestApplyIMAPSizeLimit(t *testing.T) {
	const sentinel int64 = -1

	// A clean read applies the stored cap verbatim.
	got := sentinel
	applyIMAPSizeLimit(
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{IMAPLiteralBytes: 4096}, true, nil
		},
		func(n int64) { got = n })
	if got != 4096 {
		t.Errorf("applied cap = %d, want 4096", got)
	}

	// A read error must NOT call the setter — the running cap stays as it is.
	got = sentinel
	applyIMAPSizeLimit(
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{}, false, errors.New("db down")
		},
		func(n int64) { got = n })
	if got != sentinel {
		t.Errorf("setter called on read error (got %d); the cap must be left unchanged", got)
	}

	// No stored row must NOT call the setter — the built-in default stands.
	got = sentinel
	applyIMAPSizeLimit(
		func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil },
		func(n int64) { got = n })
	if got != sentinel {
		t.Errorf("setter called with no stored row (got %d); the default must stand", got)
	}
}
