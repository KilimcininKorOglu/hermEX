package mapihttp

import (
	"encoding/binary"
	"net/http"
	"testing"

	"hermex/internal/objectstore"
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

// closeBody closes a response body and fails the test when the close fails.
func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}

// closeStore closes a mailbox store and fails the test when the close fails.
func closeStore(t *testing.T, st *objectstore.Store) {
	t.Helper()
	if err := st.Close(); err != nil {
		t.Errorf("close store: %v", err)
	}
}

// mustRopOK stops the test unless a ROP response buffer starts with the expected
// RopId and a zero ReturnValue ([MS-OXCROPS] 2.2.1: RopId, then the handle index
// byte, then the 4-byte ReturnValue), so a failed ROP cannot pass as a handle.
func mustRopOK(t *testing.T, rops []byte, ropID byte, what string) {
	t.Helper()
	if len(rops) < 6 || rops[0] != ropID {
		t.Fatalf("%s response = % x, want RopId %#x", what, rops, ropID)
	}
	if ec := binary.LittleEndian.Uint32(rops[2:]); ec != 0 {
		t.Fatalf("%s ReturnValue = %#x, want 0", what, ec)
	}
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
