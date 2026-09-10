package publicfolder

import (
	"slices"
	"testing"

	"hermex/internal/mapi"
)

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

// wantNames fails the test unless the visible folders are exactly those named.
func wantNames(t *testing.T, got []Folder, want []string, what string) {
	t.Helper()
	if !slices.Equal(names(got), want) {
		t.Errorf("%s = %v, want %v", what, names(got), want)
	}
}

// wantPostRight fails the test unless the caller may post to the named folder.
func wantPostRight(t *testing.T, got []Folder, name string) {
	t.Helper()
	for _, f := range got {
		if f.DisplayName != name {
			continue
		}
		if f.Rights&mapi.FrightsCreate == 0 {
			t.Errorf("the post right on %s is missing: rights=%#x", name, f.Rights)
		}
		return
	}
	t.Errorf("%s is not visible at all, so its post right cannot hold", name)
}
