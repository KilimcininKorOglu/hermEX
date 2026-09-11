package main

import (
	"errors"
	"testing"

	"hermex/internal/directory"
)

// applied records what one applyEWSSizeLimit pass set, so a test can state which
// setters ran and with what. The sentinel is a value no caller would store, so an
// untouched field proves the setter was never called.
type applied struct {
	body, targets, subTimeout int64
}

const sentinel int64 = -1

// runApply drives one pass and reports what it set.
func runApply(read func() (directory.SizeLimits, bool, error)) applied {
	got := applied{sentinel, sentinel, sentinel}
	applyEWSSizeLimit(nil, read,
		func(n int64) { got.body = n },
		func(n int64) { got.targets = n },
		func(n int64) { got.subTimeout = n })
	return got
}

// TestApplyEWSSizeLimit proves the SOAP request-body cap is applied only on a clean
// read; a read error or a missing row leaves it untouched, so a transient directory
// failure never shrinks the running cap to zero.
func TestApplyEWSSizeLimit(t *testing.T) {
	got := runApply(func() (directory.SizeLimits, bool, error) {
		return directory.SizeLimits{EWSRequestBytes: 8192}, true, nil
	})
	if got.body != 8192 {
		t.Errorf("applied cap = %d, want 8192", got.body)
	}

	got = runApply(func() (directory.SizeLimits, bool, error) {
		return directory.SizeLimits{}, false, errors.New("db down")
	})
	if got.body != sentinel {
		t.Errorf("setter called on read error (got %d); the cap must be left unchanged", got.body)
	}

	got = runApply(func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil })
	if got.body != sentinel {
		t.Errorf("setter called with no stored row (got %d); the default must stand", got.body)
	}
}

// TestApplyEWSSubscriptionTimeout proves the subscription idle timeout travels the
// same path as the byte caps. It is the setting that decides how long a streaming
// client may be away before the server drops its subscription, so an operator's
// value must reach the daemon, and a failed read must leave the running value alone
// rather than reset it to the built-in default.
func TestApplyEWSSubscriptionTimeout(t *testing.T) {
	got := runApply(func() (directory.SizeLimits, bool, error) {
		return directory.SizeLimits{EWSSubscriptionTimeoutMinutes: 720}, true, nil
	})
	if got.subTimeout != 720 {
		t.Errorf("applied subscription timeout = %d, want 720", got.subTimeout)
	}

	got = runApply(func() (directory.SizeLimits, bool, error) {
		return directory.SizeLimits{}, false, errors.New("db down")
	})
	if got.subTimeout != sentinel {
		t.Errorf("subscription-timeout setter called on read error (got %d); it must be left unchanged", got.subTimeout)
	}

	got = runApply(func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil })
	if got.subTimeout != sentinel {
		t.Errorf("subscription-timeout setter called with no stored row (got %d); the default must stand", got.subTimeout)
	}
}
