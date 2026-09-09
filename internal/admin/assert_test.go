package admin

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// This file is the package's assertion vocabulary. A test states one fact per call,
// so a failure names the fact that broke rather than the condition that evaluated.

// wantStatus fails the test unless the response carries the expected status code,
// and closes the body. It stops the test, because every later assertion reads state
// the request was supposed to produce.
func wantStatus(t *testing.T, resp *http.Response, want int, what string) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("%s: status %d, want %d", what, resp.StatusCode, want)
	}
}

// wantBody asserts the response status and returns its body for the content
// assertions that follow.
func wantBody(t *testing.T, resp *http.Response, want int, what string) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s: read body: %v", what, err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s: status %d, want %d\n%s", what, resp.StatusCode, want, body)
	}
	return string(body)
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

// mustNoErr stops the test on an error from a setup step.
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
