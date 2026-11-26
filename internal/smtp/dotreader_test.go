package smtp

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

// TestDotReaderHoldsABoundedBuffer feeds one 1 MiB line with no line feed in
// sight. The DATA phase is open to any unauthenticated sender, so the reader may
// hold at most one buffer of the line at a time: reading the whole line first
// lets one sender grow the server's memory without limit, and the size cap
// cannot help because it counts only what the reader hands on.
func TestDotReaderHoldsABoundedBuffer(t *testing.T) {
	long := strings.Repeat("x", 1<<20)
	d := newDotReader(bufio.NewReader(strings.NewReader(long + "\r\n.\r\n")))
	p := make([]byte, 1)
	if _, err := d.Read(p); err != nil {
		t.Fatal(err)
	}
	if got := len(d.buf) + 1; got > 64<<10 {
		t.Errorf("one read buffered %d bytes of a single line, want a bounded chunk", got)
	}
	rest, err := io.ReadAll(d)
	if err != nil {
		t.Fatal(err)
	}
	if got := 1 + len(rest); got != len(long)+2 {
		t.Errorf("decoded body is %d bytes, want %d", got, len(long)+2)
	}
}

// TestDotReaderDecodesAcrossChunks takes its rules from RFC 5321: a line of a
// single "." ends the data (4.1.1.4), and a leading "." that the sender doubled
// is removed (4.5.2). Both apply at the start of a line only, so a "." that falls
// at the start of the second chunk of a long line is data and stays.
func TestDotReaderDecodesAcrossChunks(t *testing.T) {
	long := strings.Repeat("y", 4096)
	in := "..lead\r\n" + long + ".\r\n" + long + "..mid\r\n.\r\nAFTER"
	want := ".lead\r\n" + long + ".\r\n" + long + "..mid\r\n"
	br := bufio.NewReader(strings.NewReader(in))
	got, err := io.ReadAll(newDotReader(br))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("decoded body differs: got %d bytes, want %d", len(got), len(want))
	}
	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read past the terminator: %v", err)
	}
	if string(rest) != "AFTER" {
		t.Errorf("after the terminator the stream holds %q, want AFTER", rest)
	}
}
