package webmail2api

import (
	"strings"
	"testing"
)

// TestLogSafeStripsLineBreaks is the defect. The stub handler logged r.URL.Path
// verbatim, and that path is percent-decoded: a request for "/api/v1/x%0a..."
// arrives with a real newline in it. Writing that to a line-oriented log lets the
// caller append whatever lines they like, so a forged entry is indistinguishable
// from a real one.
func TestLogSafeStripsLineBreaks(t *testing.T) {
	forged := "/api/v1/x\n2026/01/01 00:00:00 webmail2api: admin granted to attacker@evil.test"

	got := logSafe(forged)

	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("the value still breaks the line: %q", got)
	}
	if strings.Contains(got, "\n2026") {
		t.Errorf("the forged entry survived: %q", got)
	}
	// The real path must still be readable, or the log stops answering what was hit.
	if !strings.HasPrefix(got, "/api/v1/x") {
		t.Errorf("the genuine prefix was lost: %q", got)
	}
}

// TestLogSafeStripsOtherControlCharacters covers the rest of the class. A terminal
// reading the log honours escape sequences, so NUL, backspace and ESC are the same
// problem as CR and LF.
func TestLogSafeStripsOtherControlCharacters(t *testing.T) {
	for _, in := range []string{"/a\x00b", "/a\x1b[2Jb", "/a\x08b", "/a\tb", "/a\x7fb"} {
		got := logSafe(in)
		for _, r := range got {
			if r < 0x20 || r == 0x7f {
				t.Errorf("logSafe(%q) = %q still carries control character %#U", in, got, r)
			}
		}
	}
}

// TestLogSafeLeavesOrdinaryPathsAlone guards the other direction: a sanitizer that
// mangles normal input makes the log harder to read than no sanitizer at all.
func TestLogSafeLeavesOrdinaryPathsAlone(t *testing.T) {
	for _, in := range []string{
		"/api/v1/messages",
		"/api/v1/folders/Inbox/messages?limit=50",
		"/api/v1/contacts/ünïcode-名前",
	} {
		if got := logSafe(in); got != in {
			t.Errorf("logSafe(%q) = %q, want it unchanged", in, got)
		}
	}
}

// TestLogSafeBoundsLength stops one request from filling the log. A path can be
// kilobytes long, and the stub handler is reachable on every unrouted API path.
func TestLogSafeBoundsLength(t *testing.T) {
	got := logSafe("/api/v1/" + strings.Repeat("a", 4096))
	if len(got) > logSafeMax+3 {
		t.Errorf("length = %d, want it bounded near %d", len(got), logSafeMax)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("a truncated value does not say so: %q", got[max(0, len(got)-16):])
	}
}
