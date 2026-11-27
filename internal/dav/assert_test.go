package dav

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// mustCreate stores msg in folder fid and fails the test unless the store
// reports a real object id for it.
func mustCreate(t *testing.T, st *objectstore.Store, fid int64, msg *oxcmail.Message) {
	t.Helper()
	id, err := st.CreateMessage(fid, msg)
	if err != nil {
		t.Fatalf("store the message: %v", err)
	}
	if id == 0 {
		t.Fatal("the store returned object id 0 for a created message")
	}
}

// mustClose closes c and fails the test when the close reports an error, so a
// store or response body that fails to close is never silently dropped.
func mustClose(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

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
