package imap

import "testing"

// TestPartialRefusesAnOutOfRangeNumber is the load-bearing case: the octet window
// is two numbers the client sends, and they reach an expression that slices the
// message body. A negative number, or one wide enough to wrap the addition, made
// that slice go out of range, and the panic took the whole daemon down with every
// other user's connection. RFC 3501 writes both as 32-bit unsigned, so anything
// else is refused here.
func TestPartialRefusesAnOutOfRangeNumber(t *testing.T) {
	for _, spec := range []string{
		"<-1.10>",                 // negative start
		"<1.-10>",                 // negative count
		"<1.9223372036854775807>", // a count that wraps start+count
		"<9223372036854775807.1>", // a start past the 32-bit width
		"<4294967296.1>",          // one past the 32-bit ceiling
		"<1.4294967296>",          // the same for the count
	} {
		if p, err := parsePartial(spec); err == nil {
			t.Errorf("parsePartial(%q) = %v, want a protocol error", spec, p)
		}
	}
}

// TestPartialAcceptsTheSpecRange keeps the refusal from taking the values a client
// legitimately sends with it.
func TestPartialAcceptsTheSpecRange(t *testing.T) {
	for _, c := range []struct {
		spec         string
		start, count int
	}{
		{"<0.1>", 0, 1},
		{"<10.4096>", 10, 4096},
		{"<4294967295.4294967295>", 4294967295, 4294967295},
	} {
		p, err := parsePartial(c.spec)
		if err != nil {
			t.Errorf("parsePartial(%q) failed: %v", c.spec, err)
			continue
		}
		if p[0] != c.start || p[1] != c.count {
			t.Errorf("parsePartial(%q) = %v, want [%d %d]", c.spec, p, c.start, c.count)
		}
	}
}

// TestApplyPartialNeverSlicesOutOfRange is the second half: the window itself must
// hold whatever it is given, because it is the expression that indexes the body.
func TestApplyPartialNeverSlicesOutOfRange(t *testing.T) {
	data := []byte("0123456789abcdef")
	for _, p := range [][2]int{
		{-1, 10},                 // a negative start
		{1, -10},                 // a negative count
		{1, 9223372036854775807}, // start+count wraps
		{9223372036854775807, 1}, // a start past the data
		{4294967295, 4294967295}, // the spec's own ceiling
		{0, 4294967295},          // a count far past the data
	} {
		part := p
		got := applyPartial(data, &part)
		if len(got) > len(data) {
			t.Errorf("applyPartial(%v) returned %d bytes, more than the data holds", part, len(got))
		}
	}
	// The ordinary window still works.
	if got := applyPartial(data, &[2]int{2, 4}); string(got) != "2345" {
		t.Errorf("applyPartial([2 4]) = %q, want \"2345\"", got)
	}
	if got := applyPartial(data, &[2]int{10, 100}); string(got) != "abcdef" {
		t.Errorf("applyPartial([10 100]) = %q, want the tail", got)
	}
}

// TestFetchSurvivesABadPartial drives it through the command path: the refusal must
// reach the client as a status, and the connection must go on serving. Before the
// bound, this command panicked inside the FETCH render and took the daemon with it.
func TestFetchSurvivesABadPartial(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX")

	for _, spec := range []string{"<1.9223372036854775807>", "<-1.10>", "<4294967296.1>"} {
		if _, status := c.do("a3", "FETCH 1 BODY[]"+spec); status != "BAD" {
			t.Errorf("FETCH with %s: status %s, want BAD", spec, status)
		}
	}
	if _, status := c.do("a4", "NOOP"); status != "OK" {
		t.Errorf("the connection must keep serving, NOOP status %s", status)
	}
}
