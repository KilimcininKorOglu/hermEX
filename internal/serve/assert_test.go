package serve

import (
	"strings"
	"testing"

	"hermex/internal/logging"
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

// wantField fails the test unless a logged event carries the expected field.
func wantField(t *testing.T, e logging.Event, key string, want any, what string) {
	t.Helper()
	if e.Fields[key] != want {
		t.Errorf("%s = %v, want %v", what, e.Fields[key], want)
	}
}

// wantNoSecret fails the test when a password reached the rendered event, which
// is what an operator would actually see.
func wantNoSecret(t *testing.T, e logging.Event, secret string) {
	t.Helper()
	var rendered strings.Builder
	logging.NewStderrSink(&rendered).Write(e)
	if strings.Contains(rendered.String(), secret) {
		t.Errorf("the password %q leaked into the logged event", secret)
	}
}
