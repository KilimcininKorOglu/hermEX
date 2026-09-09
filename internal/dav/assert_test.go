package dav

import (
	"net/http"
	"strings"
	"testing"
)

// This file is the package's assertion vocabulary. A test states one fact per call,
// so a failure names the fact that broke rather than the condition that evaluated.

// wantStatus fails the test unless the response carries the expected status code.
// It stops the test, because every later assertion reads a body that was never
// produced.
func wantStatus(t *testing.T, resp *http.Response, want int, what string) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s: status %d, want %d", what, resp.StatusCode, want)
	}
}

// wantContains fails the test unless the body carries the expected fragment.
func wantContains(t *testing.T, body, sub, what string) {
	t.Helper()
	if !strings.Contains(body, sub) {
		t.Errorf("%s: %q missing from\n%s", what, sub, body)
	}
}

// wantNotContains fails the test when the body carries a fragment it must not.
func wantNotContains(t *testing.T, body, sub, what string) {
	t.Helper()
	if strings.Contains(body, sub) {
		t.Errorf("%s: %q present in\n%s", what, sub, body)
	}
}
