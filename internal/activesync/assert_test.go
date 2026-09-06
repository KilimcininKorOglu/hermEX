package activesync

import "testing"

// The ActiveSync tests assert against many small values at once: a device record
// has eight stamped fields, a Devices row has five, and a WBXML response has one
// element per property. Written as one composite if per assertion, a failure
// names the whole struct and leaves the reader to find which field moved.
//
// The helpers here name the field in the failure message, so a test body reads as
// the list of facts it pins.

// wantEq fails when got differs from want, naming the field in the message.
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
