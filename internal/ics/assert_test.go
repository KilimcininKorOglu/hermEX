package ics

import (
	"testing"

	"hermex/internal/mapi"
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

// wantName fails the test unless a decoded property carries the expected name,
// stopping the test when it carries none at all.
func wantName(t *testing.T, got *mapi.PropertyName, want mapi.PropertyName, what string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: no property name decoded", what)
	}
	wantEq(t, got.Kind, want.Kind, what+" kind")
	wantEq(t, got.GUID, want.GUID, what+" GUID")
	wantEq(t, got.LID, want.LID, what+" LID")
	wantEq(t, got.Name, want.Name, what+" name")
}
