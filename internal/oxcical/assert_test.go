package oxcical

import (
	"strings"
	"testing"
	"time"
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

// wantTrue fails the test unless the condition holds.
func wantTrue(t *testing.T, got bool, what string) {
	t.Helper()
	if !got {
		t.Errorf("%s: false, want true", what)
	}
}

// wantFalse fails the test when the condition holds.
func wantFalse(t *testing.T, got bool, what string) {
	t.Helper()
	if got {
		t.Errorf("%s: true, want false", what)
	}
}

// mustNoErr stops the test on an error from a conversion under test.
func mustNoErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantContains fails the test unless the rendered object carries the expected line.
func wantContains(t *testing.T, body, sub, what string) {
	t.Helper()
	if !strings.Contains(body, sub) {
		t.Errorf("%s: %q missing from\n%s", what, sub, body)
	}
}

// wantProp fails the test unless a component's property carries the expected text.
func wantProp(t *testing.T, c *icomp, name, want string) {
	t.Helper()
	wantEq(t, c.propText(name), want, name)
}

// wantPropValue fails the test unless a component's property carries the expected
// raw value, the form a wire reader sees before unescaping.
func wantPropValue(t *testing.T, c *icomp, name, want string) {
	t.Helper()
	l := c.prop(name)
	if l == nil {
		t.Errorf("%s: absent, want %q", name, want)
		return
	}
	wantEq(t, l.value, want, name)
}

// wantTime fails the test unless the instant matches, comparing in UTC so a
// difference in location never reads as a difference in time.
func wantTime(t *testing.T, got, want time.Time, what string) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s = %s, want %s", what, got.UTC(), want.UTC())
	}
}
