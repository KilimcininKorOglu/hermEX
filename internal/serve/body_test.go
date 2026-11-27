package serve

import (
	"errors"
	"strings"
	"testing"
)

// TestReadBodyRefusesWhatItCannotReadWhole reads bodies around the cap: one byte
// over is refused, not cut to a prefix the caller would parse as the request;
// exactly at the cap and empty are read whole.
func TestReadBodyRefusesWhatItCannotReadWhole(t *testing.T) {
	// The refusal hands back no prefix at all, so a caller that ignores the error
	// still has nothing to parse.
	if got, err := ReadBody(strings.NewReader("abcdef"), 5); !errors.Is(err, ErrBodyTooLarge) || got != nil {
		t.Errorf("6 bytes under a 5-byte cap = %q, %v, want nil and ErrBodyTooLarge", got, err)
	}
	if got, err := ReadBody(strings.NewReader("abcde"), 5); err != nil || string(got) != "abcde" {
		t.Errorf("5 bytes under a 5-byte cap = %q, %v, want the whole body", got, err)
	}
	if got, err := ReadBody(strings.NewReader(""), 5); err != nil || len(got) != 0 {
		t.Errorf("empty body = %q, %v, want empty", got, err)
	}
}
