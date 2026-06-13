package imap

import (
	"testing"

	"hermex/internal/objectstore"
)

func TestFlagBitCaseInsensitive(t *testing.T) {
	if bit, ok := flagBit(`\seen`); !ok || bit != objectstore.FlagSeen {
		t.Errorf("flagBit(\\seen) = %d, %v; want %d, true", bit, ok, objectstore.FlagSeen)
	}
	if _, ok := flagBit(`$label1`); ok {
		t.Errorf("flagBit(keyword) ok = true, want false (keywords not persisted)")
	}
}

func TestFormatFlags(t *testing.T) {
	// Distinct bits ordered as the table declares them; \Recent appended last.
	got := formatFlags(objectstore.FlagAnswered|objectstore.FlagSeen, true)
	if got != `\Seen \Answered \Recent` {
		t.Errorf("formatFlags = %q, want %q", got, `\Seen \Answered \Recent`)
	}
	if got := formatFlags(0, false); got != "" {
		t.Errorf("formatFlags(none) = %q, want empty", got)
	}
}

func TestApplyFlagNames(t *testing.T) {
	base := objectstore.FlagSeen | objectstore.FlagFlagged
	// '+' sets, '-' clears, replace overwrites; unknown keywords are ignored.
	if got := applyFlagNames(base, '+', []string{`\Deleted`, "Junk"}); got != base|objectstore.FlagDeleted {
		t.Errorf("+FLAGS = %d, want %d", got, base|objectstore.FlagDeleted)
	}
	if got := applyFlagNames(base, '-', []string{`\Seen`}); got != objectstore.FlagFlagged {
		t.Errorf("-FLAGS = %d, want %d", got, objectstore.FlagFlagged)
	}
	if got := applyFlagNames(base, ' ', []string{`\Draft`}); got != objectstore.FlagDraft {
		t.Errorf("replace = %d, want %d", got, objectstore.FlagDraft)
	}
}
