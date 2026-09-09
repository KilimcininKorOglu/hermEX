package pop3

import (
	"net/textproto"
	"strings"
	"testing"
)

// This file is the package's assertion vocabulary. A test states one fact per call,
// so a failure names the fact that broke rather than the condition that evaluated.

// wantEq fails the test unless the value equals what the caller expected.
func wantEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// wantContains fails the test unless the body carries the expected fragment.
func wantContains(t *testing.T, body, sub, what string) {
	t.Helper()
	if !strings.Contains(body, sub) {
		t.Errorf("%s: %q missing from\n%s", what, sub, body)
	}
}

// wantLinePrefix fails the test unless a protocol line begins with prefix.
func wantLinePrefix(t *testing.T, line, prefix, what string) {
	t.Helper()
	if !strings.HasPrefix(line, prefix) {
		t.Errorf("%s = %q, want it to begin with %q", what, line, prefix)
	}
}

// wantReply reads one reply line and stops the test unless it carries the status
// prefix the caller expected.
func wantReply(t *testing.T, r *textproto.Reader, prefix string) string {
	t.Helper()
	l, err := r.ReadLine()
	if err != nil {
		t.Fatalf("read a %s reply: %v", prefix, err)
	}
	if !strings.HasPrefix(l, prefix) {
		t.Fatalf("reply = %q, want it to begin with %s", l, prefix)
	}
	return l
}

// readMultiline reads a dot-terminated multiline response.
func readMultiline(t *testing.T, r *textproto.Reader) []string {
	t.Helper()
	var lines []string
	for {
		l, err := r.ReadLine()
		if err != nil {
			t.Fatalf("read a multiline response: %v", err)
		}
		if l == "." {
			return lines
		}
		lines = append(lines, l)
	}
}
