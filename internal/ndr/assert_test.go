package ndr

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
// decoder returns a function that passes a decoded value through and fails the
// test when its read failed, so a short or malformed PDU stops the test instead
// of shifting every field read after it.
func decoder[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		return v
	}
}

func wantEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// mustCount stops the test unless a decoded list holds the expected number of
// elements, because the following assertions index into it.
func mustCount(t *testing.T, got, want int, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", what, got, want)
	}
}
