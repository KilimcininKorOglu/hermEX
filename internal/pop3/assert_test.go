package pop3

import (
	"errors"
	"io"
	"net"
	"net/textproto"
	"strings"
	"testing"
)

// serveOn starts srv on a loopback listener and returns it. At cleanup it closes
// the listener and fails the test if Serve stopped for any reason other than that
// close, so an accept-loop failure is never lost in a discarded return value.
func serveOn(t *testing.T, srv *Server) net.Listener {
	t.Helper()
	return serveWrapped(t, srv, func(l net.Listener) net.Listener { return l })
}

// serveWrapped is serveOn for a server that accepts on wrap(ln), such as an
// implicit-TLS listener; it returns the raw listener, whose address clients dial.
func serveWrapped(t *testing.T, srv *Server, wrap func(net.Listener) net.Listener) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(wrap(ln)) }()
	t.Cleanup(func() {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close the listener: %v", err)
		}
		if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("Serve stopped with %v", err)
		}
	})
	return ln
}

// mustClose closes c and fails the test when the close reports an error.
func mustClose(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// This file is the package's assertion vocabulary. A test states one fact per call,
// so a failure names the fact that broke rather than the condition that evaluated.

// wantEq fails the test unless the value equals what the caller expected.
func wantEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// wantContains fails the test unless the body carries the expected fragment.
func wantContains(t *testing.T, body, sub, what string) {
	t.Helper()
	if !strings.Contains(body, sub) {
		t.Errorf("%s: %q missing from\n%s", what, sub, body)
	}
}

// wantLinePrefix fails the test unless a protocol line begins with prefix.
func wantLinePrefix(t *testing.T, line, prefix, what string) {
	t.Helper()
	if !strings.HasPrefix(line, prefix) {
		t.Errorf("%s = %q, want it to begin with %q", what, line, prefix)
	}
}

// wantReply reads one reply line and stops the test unless it carries the status
// prefix the caller expected.
func wantReply(t *testing.T, r *textproto.Reader, prefix string) string {
	t.Helper()
	l, err := r.ReadLine()
	if err != nil {
		t.Fatalf("read a %s reply: %v", prefix, err)
	}
	if !strings.HasPrefix(l, prefix) {
		t.Fatalf("reply = %q, want it to begin with %s", l, prefix)
	}
	return l
}

// readMultiline reads a dot-terminated multiline response.
func readMultiline(t *testing.T, r *textproto.Reader) []string {
	t.Helper()
	var lines []string
	for {
		l, err := r.ReadLine()
		if err != nil {
			t.Fatalf("read a multiline response: %v", err)
		}
		if l == "." {
			return lines
		}
		lines = append(lines, l)
	}
}
