package mta

import (
	"errors"
	"testing"

	"hermex/internal/directory"
)

// TestUnsavedLimitFallsBackToTheBuiltInCeiling is the unbounded-inbound defect.
// Inbound SMTP buffers the whole body before it is parsed, scanned and scored, and
// on a fresh install no size row exists. Treating that as "no limit" left the
// widest unauthenticated surface open to one anonymous peer.
func TestUnsavedLimitFallsBackToTheBuiltInCeiling(t *testing.T) {
	SetMaxMessageSize(1) // a value the apply must move off
	ApplyMessageSizeSettings("test", nil, func() (MessageSizeSettings, bool, error) {
		return MessageSizeSettings{}, false, nil
	})
	if got := maxMessageBytes.Load(); got != directory.DefaultMaxInboundBytes {
		t.Errorf("with nothing saved the limit is %d, want the built-in ceiling %d",
			got, int64(directory.DefaultMaxInboundBytes))
	}
}

// TestStoredZeroStaysUnlimited keeps the operator's own choice intact. Saving 0 is
// a decision the panel offers, and it must not be overridden by the ceiling that
// exists only for the case where nothing was ever chosen.
func TestStoredZeroStaysUnlimited(t *testing.T) {
	SetMaxMessageSize(1)
	ApplyMessageSizeSettings("test", nil, func() (MessageSizeSettings, bool, error) {
		return MessageSizeSettings{MaxInboundBytes: 0}, true, nil
	})
	if got := maxMessageBytes.Load(); got != 0 {
		t.Errorf("a stored 0 gave limit %d, want 0 (the operator chose no limit)", got)
	}
}

// TestStoredLimitIsApplied is the ordinary path.
func TestStoredLimitIsApplied(t *testing.T) {
	const tenMiB = 10 << 20
	SetMaxMessageSize(1)
	ApplyMessageSizeSettings("test", nil, func() (MessageSizeSettings, bool, error) {
		return MessageSizeSettings{MaxInboundBytes: tenMiB}, true, nil
	})
	if got := maxMessageBytes.Load(); got != tenMiB {
		t.Errorf("stored limit gave %d, want %d", got, int64(tenMiB))
	}
}

// TestReadFailureLeavesTheLimitAlone keeps a settings outage from changing what
// the server accepts, in either direction.
func TestReadFailureLeavesTheLimitAlone(t *testing.T) {
	const current = 7 << 20
	SetMaxMessageSize(current)
	ApplyMessageSizeSettings("test", nil, func() (MessageSizeSettings, bool, error) {
		return MessageSizeSettings{}, false, errors.New("database unavailable")
	})
	if got := maxMessageBytes.Load(); got != current {
		t.Errorf("a read failure changed the limit to %d, want it left at %d", got, int64(current))
	}
}
