package imap

import (
	"testing"

	"hermex/internal/store"
)

func TestFlagBitCaseInsensitive(t *testing.T) {
	if bit, ok := flagBit(`\seen`); !ok || bit != store.FlagSeen {
		t.Errorf("flagBit(\\seen) = %d, %v; want %d, true", bit, ok, store.FlagSeen)
	}
	if _, ok := flagBit(`$label1`); ok {
		t.Errorf("flagBit(keyword) ok = true, want false (keywords not persisted)")
	}
}

func TestFormatFlags(t *testing.T) {
	// Distinct bits ordered as the table declares them; \Recent appended last.
	got := formatFlags(store.FlagAnswered|store.FlagSeen, true)
	if got != `\Seen \Answered \Recent` {
		t.Errorf("formatFlags = %q, want %q", got, `\Seen \Answered \Recent`)
	}
	if got := formatFlags(0, false); got != "" {
		t.Errorf("formatFlags(none) = %q, want empty", got)
	}
}

func TestApplyFlagNames(t *testing.T) {
	base := store.FlagSeen | store.FlagFlagged
	// '+' sets, '-' clears, replace overwrites; unknown keywords are ignored.
	if got := applyFlagNames(base, '+', []string{`\Deleted`, "Junk"}); got != base|store.FlagDeleted {
		t.Errorf("+FLAGS = %d, want %d", got, base|store.FlagDeleted)
	}
	if got := applyFlagNames(base, '-', []string{`\Seen`}); got != store.FlagFlagged {
		t.Errorf("-FLAGS = %d, want %d", got, store.FlagFlagged)
	}
	if got := applyFlagNames(base, ' ', []string{`\Draft`}); got != store.FlagDraft {
		t.Errorf("replace = %d, want %d", got, store.FlagDraft)
	}
}
