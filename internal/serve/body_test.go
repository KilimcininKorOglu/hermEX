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
	if _, err := ReadBody(strings.NewReader("abcdef"), 5); !errors.Is(err, ErrBodyTooLarge) {
		t.Errorf("6 bytes under a 5-byte cap = %v, want ErrBodyTooLarge", err)
	}
	if got, err := ReadBody(strings.NewReader("abcde"), 5); err != nil || string(got) != "abcde" {
		t.Errorf("5 bytes under a 5-byte cap = %q, %v, want the whole body", got, err)
	}
	if got, err := ReadBody(strings.NewReader(""), 5); err != nil || len(got) != 0 {
		t.Errorf("empty body = %q, %v, want empty", got, err)
	}
}
