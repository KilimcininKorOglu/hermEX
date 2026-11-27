package smtp

import (
	"errors"
	"io"
	"net"
	"net/textproto"
	"strings"
	"testing"
)

// This file is the package's assertion vocabulary. A test states one fact per call,
// so a failure names the fact that broke rather than the condition that evaluated.

// serveOn starts srv on a loopback listener and returns it. At cleanup it closes
// the listener and fails the test if Serve stopped for any reason other than that
// close, so an accept-loop failure is never lost in a discarded return value.
func serveOn(t *testing.T, srv *Server) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	t.Cleanup(func() {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close listener: %v", err)
		}
		if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("Serve stopped with %v", err)
		}
	})
	return ln
}

// startServer starts srv (with pre-registered listeners) in the background, and
// closes all listeners at cleanup while verifying Start returned no unexpected error.
func startServer(t *testing.T, srv *Server, listeners ...net.Listener) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- srv.Start() }()
	t.Cleanup(func() {
		for _, ln := range listeners {
			if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				t.Errorf("close listener: %v", err)
			}
		}
		if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("Start stopped with %v", err)
		}
	})
}

// mustClose closes c and fails the test when the close reports an unexpected error.
func mustClose(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("close: %v", err)
	}
}

// wantEq fails the test unless the value equals what the caller expected.
func wantEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// readCodedReply reads one reply, stopping the test unless it carries the
// expected status code, and returns its message text.
func readCodedReply(t *testing.T, r *textproto.Reader, code int, what string) string {
	t.Helper()
	_, msg, err := r.ReadResponse(code)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return msg
}

// wantReplyPrefix fails the test unless a reply message begins with prefix.
func wantReplyPrefix(t *testing.T, msg, prefix, what string) {
	t.Helper()
	if !strings.HasPrefix(msg, prefix) {
		t.Errorf("%s = %q, want it to begin with %q", what, msg, prefix)
	}
}
