package netline

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

// reader wraps a script the test feeds to ReadLine.
func reader(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }

// TestReadLineStripsTheTerminator proves both terminators end a line and neither
// rides along in the value.
func TestReadLineStripsTheTerminator(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"a NOOP\r\n", "a NOOP"},
		{"a NOOP\n", "a NOOP"},
		{"\r\n", ""},
	} {
		got, err := ReadLine(reader(c.in), 64)
		if err != nil || got != c.want {
			t.Errorf("ReadLine(%q) = %q, %v, want %q", c.in, got, err, c.want)
		}
	}
}

// TestReadLineRefusesALongLine is the load-bearing case: a line past the limit is
// refused rather than accumulated, which is what keeps a client that never sends a
// terminator from growing the server's memory.
func TestReadLineRefusesALongLine(t *testing.T) {
	long := strings.Repeat("x", 100) + "\r\n"

	if _, err := ReadLine(reader(long), 16); !errors.Is(err, ErrTooLong) {
		t.Errorf("err = %v, want ErrTooLong", err)
	}
}

// TestReadLineStaysInFrame proves the refusal leaves the stream at a line boundary,
// so the connection keeps serving instead of answering the tail of the refused line
// as if it were the next command.
func TestReadLineStaysInFrame(t *testing.T) {
	br := reader(strings.Repeat("x", 100) + "\r\na NOOP\r\n")

	if _, err := ReadLine(br, 16); !errors.Is(err, ErrTooLong) {
		t.Fatalf("first read err = %v, want ErrTooLong", err)
	}
	got, err := ReadLine(br, 16)
	if err != nil || got != "a NOOP" {
		t.Errorf("second read = %q, %v, want the next command", got, err)
	}
}

// TestReadLineAtTheLimit proves the limit is the longest line admitted, not the
// first one refused.
func TestReadLineAtTheLimit(t *testing.T) {
	exact := strings.Repeat("x", 16)

	got, err := ReadLine(reader(exact+"\r\n"), 16)
	if err != nil || got != exact {
		t.Errorf("a line of exactly the limit = %q, %v, want it admitted", got, err)
	}
	if _, err := ReadLine(reader(exact+"x\r\n"), 16); !errors.Is(err, ErrTooLong) {
		t.Errorf("one byte past the limit = %v, want ErrTooLong", err)
	}
}

// TestReadLineUnboundedReadsEverything proves a limit of zero is unbounded, the
// form only a test asks for.
func TestReadLineUnboundedReadsEverything(t *testing.T) {
	long := strings.Repeat("x", 10000)

	got, err := ReadLine(reader(long+"\r\n"), 0)
	if err != nil || got != long {
		t.Errorf("unbounded read = %d bytes, %v, want %d", len(got), err, len(long))
	}
}

// TestReadLineReportsTheReadError proves a closed connection is reported as itself,
// not as a refusal.
func TestReadLineReportsTheReadError(t *testing.T) {
	if _, err := ReadLine(reader("no terminator"), 64); err == nil || errors.Is(err, ErrTooLong) {
		t.Errorf("err = %v, want the underlying read error", err)
	}
}
