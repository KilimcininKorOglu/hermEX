package mtasts

import "testing"

// This file is the package's assertion vocabulary. A test states one fact per call,
// so a failure names the fact that broke rather than the condition that evaluated.

// mustNoErr stops the test when a step failed.
func mustNoErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantEq fails the test unless the value equals what the caller expected.
func wantEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// wantNoPolicy fails the test unless the lookup produced no policy at all.
func wantNoPolicy(t *testing.T, got *Policy, what string) {
	t.Helper()
	if got != nil {
		t.Errorf("%s = %+v, want none", what, got)
	}
}
