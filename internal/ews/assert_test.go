package ews

import (
	"strings"
	"testing"
)

// The EWS tests assert against SOAP responses, and a response carries many small
// facts at once: a response code, a handful of elements, and the values inside
// them. Written as one composite if per assertion, a failure names the whole
// document and leaves the reader to find which element moved.
//
// The helpers here name the fact in the failure message, so a test body reads as
// the list of things it pins.

// wantEq fails when got differs from want, naming the fact in the message.
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

// wantContains fails when the response body does not carry the given fragment.
func wantContains(t *testing.T, label, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Errorf("%s: the response does not carry %q:\n%s", label, want, body)
	}
}

// wantNotContains fails when the response body carries a fragment it must not.
func wantNotContains(t *testing.T, label, body, unwanted string) {
	t.Helper()
	if strings.Contains(body, unwanted) {
		t.Errorf("%s: the response carries %q, which it must not:\n%s", label, unwanted, body)
	}
}
