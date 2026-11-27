package imap

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"hermex/internal/logging"
	"hermex/internal/objectstore"
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

// mustClose closes c and fails the test when the close reports an unexpected error.
func mustClose(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("close: %v", err)
	}
}

// mustAppend appends a message to fid and fails the test on error or zero UID.
func mustAppend(t *testing.T, st *objectstore.Store, fid int64, raw string, when time.Time, flags int64) {
	t.Helper()
	info, err := st.AppendMessage(fid, []byte(raw), when, flags)
	mustNoErr(t, "append message", err)
	if info.UID == 0 {
		t.Fatal("appended message returned UID 0")
	}
}

// findEvent returns the first captured event with the given name, or nil when absent.
func findEvent(events []logging.Event, name string) *logging.Event {
	for i := range events {
		if events[i].Name == name {
			return &events[i]
		}
	}
	return nil
}

// The IMAP tests assert against protocol transcripts, and one exchange carries
// many small facts at once: the tagged status, the untagged lines, and the values
// inside them. Written as one composite if per assertion, a failure names the
// whole transcript and leaves the reader to find which line moved.
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

// mustCreateFolder creates a top-level folder and fails the test when the store
// refuses it, so a grant is never applied to folder id 0.
func mustCreateFolder(t *testing.T, st *objectstore.Store, name string) int64 {
	t.Helper()
	fid, err := st.CreateFolder(nil, name)
	mustNoErr(t, "create folder "+name, err)
	return fid
}

// wantContains fails when the transcript does not carry the given fragment.
func wantContains(t *testing.T, label, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("%s: %q does not carry %q", label, got, want)
	}
}

// wantNotContains fails when the transcript carries a fragment it must not.
func wantNotContains(t *testing.T, label, got, unwanted string) {
	t.Helper()
	if strings.Contains(got, unwanted) {
		t.Errorf("%s: %q carries %q, which it must not", label, got, unwanted)
	}
}

// wantPrefix fails when a line does not start with the expected prefix, which is
// how a tagged IMAP response reports its status.
func wantPrefix(t *testing.T, label, got, prefix string) {
	t.Helper()
	if !strings.HasPrefix(got, prefix) {
		t.Errorf("%s = %q, want it to start with %q", label, got, prefix)
	}
}
