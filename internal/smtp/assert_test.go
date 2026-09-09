package smtp

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

// readCodedReply reads one reply, stopping the test unless it carries the
// expected status code, and returns its message text.
func readCodedReply(t *testing.T, r *textproto.Reader, code int, what string) string {
	t.Helper()
	_, msg, err := r.ReadResponse(code)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return msg
}

// wantReplyPrefix fails the test unless a reply message begins with prefix.
func wantReplyPrefix(t *testing.T, msg, prefix, what string) {
	t.Helper()
	if !strings.HasPrefix(msg, prefix) {
		t.Errorf("%s = %q, want it to begin with %q", what, msg, prefix)
	}
}
