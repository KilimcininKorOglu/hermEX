package mapihttp

import (
	"net/http"
	"testing"
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

// wantResponseCode fails the test unless a MAPI/HTTP reply carries the expected
// X-ResponseCode.
func wantResponseCode(t *testing.T, resp *http.Response, want, what string) {
	t.Helper()
	wantEq(t, resp.Header.Get("X-ResponseCode"), want, what+" X-ResponseCode")
}

// mustSession posts a Bind or Connect and returns the session cookies it issued.
func mustSession(t *testing.T, resp *http.Response, what string) (sid, seq string) {
	t.Helper()
	sid, seq = cookieByName(resp, "sid"), cookieByName(resp, "sequence")
	if sid == "" || seq == "" {
		t.Fatalf("%s issued no session cookies", what)
	}
	return sid, seq
}
