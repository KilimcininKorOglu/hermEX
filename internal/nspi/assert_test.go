package nspi

import "testing"

// The NSPI tests assert against decoded wire structures, and one response
// carries many small facts at once: a result code, a row count, and the value of
// each property in each row. Written as one composite if per assertion, a
// failure names the whole structure and leaves the reader to find which field
// moved.
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
