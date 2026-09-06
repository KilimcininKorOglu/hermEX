package imap

import (
	"strings"
	"testing"
)

// The IMAP tests assert against protocol transcripts, and one exchange carries
// many small facts at once: the tagged status, the untagged lines, and the values
// inside them. Written as one composite if per assertion, a failure names the
// whole transcript and leaves the reader to find which line moved.
//
// The helpers here name the fact in the failure message, so a test body reads as
// the list of things it pins.

// wantEq fails when got differs from want, naming the fact in the message.
func wantEq[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

// mustNoErr fails the test when err is set, naming the operation.
func mustNoErr(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantContains fails when the transcript does not carry the given fragment.
func wantContains(t *testing.T, label, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("%s: %q does not carry %q", label, got, want)
	}
}

// wantNotContains fails when the transcript carries a fragment it must not.
func wantNotContains(t *testing.T, label, got, unwanted string) {
	t.Helper()
	if strings.Contains(got, unwanted) {
		t.Errorf("%s: %q carries %q, which it must not", label, got, unwanted)
	}
}

// wantPrefix fails when a line does not start with the expected prefix, which is
// how a tagged IMAP response reports its status.
func wantPrefix(t *testing.T, label, got, prefix string) {
	t.Helper()
	if !strings.HasPrefix(got, prefix) {
		t.Errorf("%s = %q, want it to start with %q", label, got, prefix)
	}
}
